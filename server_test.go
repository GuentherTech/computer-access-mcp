package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testToken = rand.Text() + rand.Text()

func testShell(t *testing.T) *powerShell {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell 7 is required")
	}
	return &powerShell{executable: pwsh, workdir: t.TempDir(), slots: make(chan struct{}, 4)}
}

func rpc(t *testing.T, endpoint, method string, params any) map[string]json.RawMessage {
	t.Helper()
	result, err := rpcRequest(endpoint, method, params)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func rpcRequest(endpoint, method string, params any) (map[string]json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	client := &http.Client{Timeout: 20 * time.Second}
	if dns := os.Getenv("TEST_CLOUDFLARE_DNS"); dns != "" {
		resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(dns, "53"))
		}}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		dialer := &net.Dialer{Resolver: resolver, Timeout: 10 * time.Second}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", address)
		}
		defer transport.CloseIdleConnections()
		client.Transport = transport
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %.300s", response.StatusCode, data)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return nil, fmt.Errorf("expected JSON response, got %q", response.Header.Get("Content-Type"))
	}
	var envelope struct {
		Result map[string]json.RawMessage `json:"result"`
		Error  json.RawMessage            `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Error) > 0 {
		return nil, fmt.Errorf("JSON-RPC error: %s", envelope.Error)
	}
	return envelope.Result, nil
}

func TestAuthentication(t *testing.T) {
	handler := newHandler(testToken, testShell(t))
	for _, token := range []string{"", "Bearer wrong", "Basic " + testToken} {
		r := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(`{}`))
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("invalid auth returned %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("browser origin returned %d", w.Code)
	}
}

func verifyMCP(t *testing.T, endpoint string) {
	t.Helper()
	init := rpc(t, endpoint, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}})
	if !strings.Contains(string(init["instructions"]), "powershell tool") {
		t.Fatal("missing server instructions")
	}
	listed := rpc(t, endpoint, "tools/list", map[string]any{})
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(listed["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "powershell" {
		t.Fatalf("unexpected tools: %s", listed["tools"])
	}
	result := rpc(t, endpoint, "tools/call", map[string]any{"name": "powershell", "arguments": map[string]any{"script": "Write-Output 'MCP connection works'"}})
	var output commandOutput
	if err := json.Unmarshal(result["structuredContent"], &output); err != nil {
		t.Fatal(err)
	}
	if output.ExitCode != 0 || !strings.Contains(output.Stdout, "MCP connection works") {
		t.Fatalf("unexpected output: %+v", output)
	}
	failed := rpc(t, endpoint, "tools/call", map[string]any{"name": "powershell", "arguments": map[string]any{"script": "throw 'expected failure'"}})
	if string(failed["isError"]) != "true" {
		t.Fatalf("failure missing isError: %v", failed)
	}
}

func TestMCPOverHTTP(t *testing.T) {
	server := httptest.NewServer(newHandler(testToken, testShell(t)))
	defer server.Close()
	verifyMCP(t, server.URL+"/mcp")
}

func TestHTTPDisconnectStopsCommand(t *testing.T) {
	p := testShell(t)
	server := httptest.NewServer(newHandler(testToken, p))
	defer server.Close()
	pidFile := filepath.Join(p.workdir, "command.pid")
	params := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "powershell", "arguments": map[string]any{"script": "Set-Content -LiteralPath './command.pid' -Value $PID; Start-Sleep 60"}}}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-06-18")
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := http.DefaultClient.Do(r)
		if err == nil {
			response.Body.Close()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	var pid string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidFile)
		pid = strings.TrimSpace(string(data))
		if pid != "" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if pid == "" {
		t.Fatal("command did not start")
	}
	cancel()
	<-done
	deadline = time.Now().Add(5 * time.Second)
	for {
		out, err := p.execute(context.Background(), commandInput{Script: "if (Get-Process -Id " + pid + " -ErrorAction SilentlyContinue) { Write-Output 'running' }"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.Stdout, "running") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command survived HTTP disconnect")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPowerShell(t *testing.T) {
	p := testShell(t)
	file := filepath.Join(p.workdir, "hello.txt")
	if err := os.WriteFile(file, []byte("Hello Joseph, café 日本語"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, script, stdout, stderr string
		exit                         int
	}{
		{"file read and Unicode", "Get-Content -Raw -LiteralPath './hello.txt'", "Hello Joseph, café 日本語", "", 0},
		{"working directory", "(Get-Location).Path", p.workdir, "", 0},
		{"PowerShell error", "Get-Content -LiteralPath './does-not-exist'", "", "does-not-exist", 1},
		{"explicit exit", "exit 7", "", "", 7},
		{"native exit", "& pwsh -NoProfile -Command 'exit 9'", "", "", 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := p.execute(context.Background(), commandInput{Script: tc.script})
			if err != nil {
				t.Fatal(err)
			}
			if out.ExitCode != tc.exit || !strings.Contains(out.Stdout, tc.stdout) || !strings.Contains(out.Stderr, tc.stderr) {
				t.Fatalf("unexpected output: %+v", out)
			}
		})
	}
	t.Run("output cap", func(t *testing.T) {
		out, err := p.execute(context.Background(), commandInput{Script: "[Console]::Write('x' * 300000)"})
		if err != nil {
			t.Fatal(err)
		}
		if !out.Truncated || len(out.Stdout) != outputLimit || out.ExitCode != 0 {
			t.Fatalf("output cap failed: length=%d truncated=%v exit=%d", len(out.Stdout), out.Truncated, out.ExitCode)
		}
	})
	t.Run("timeout kills descendants", func(t *testing.T) {
		out, err := p.execute(context.Background(), commandInput{Script: `$child = Start-Process pwsh -ArgumentList '-NoProfile', '-Command', 'Start-Sleep 60' -PassThru; [Console]::WriteLine($child.Id); Start-Sleep 60`, TimeoutSeconds: 2})
		if err != nil {
			t.Fatal(err)
		}
		if !out.TimedOut || out.ExitCode == 0 || out.DurationMS > 8000 {
			t.Fatalf("timeout failed: %+v", out)
		}
		pid := strings.TrimSpace(out.Stdout)
		if pid == "" {
			t.Fatal("child process did not start")
		}
		check, err := p.execute(context.Background(), commandInput{Script: "if (Get-Process -Id " + pid + " -ErrorAction SilentlyContinue) { throw 'child survived timeout' }"})
		if err != nil || check.ExitCode != 0 {
			t.Fatalf("child cleanup failed: %+v, %v", check, err)
		}
	})
	t.Run("background process keeps output", func(t *testing.T) {
		// The child inherits the output pipes and outlives the script. Run it
		// outside the test folder so cleanup does not race the child.
		out, err := p.execute(context.Background(), commandInput{Script: `Start-Process pwsh -ArgumentList '-NoProfile', '-Command', 'Start-Sleep 4' -NoNewWindow -WorkingDirectory ([IO.Path]::GetTempPath()); 'started'`})
		if err != nil {
			t.Fatal(err)
		}
		if out.ExitCode != 0 || out.TimedOut || !strings.Contains(out.Stdout, "started") {
			t.Fatalf("background process failed: %+v", out)
		}
	})
	t.Run("cancellation is not a timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		out, err := p.execute(ctx, commandInput{Script: "Start-Sleep 30", TimeoutSeconds: 60})
		if err != nil {
			t.Fatal(err)
		}
		if out.TimedOut || out.ExitCode == 0 {
			t.Fatalf("canceled command reported as timeout or success: %+v", out)
		}
	})
	t.Run("executable resolved per call", func(t *testing.T) {
		missing := &powerShell{lookup: func() (string, error) { return "", exec.ErrNotFound }, workdir: p.workdir, slots: make(chan struct{}, 1)}
		if _, err := missing.execute(context.Background(), commandInput{Script: "'hi'"}); err == nil || !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("missing PowerShell error: %v", err)
		}
		found := &powerShell{lookup: func() (string, error) { return p.executable, nil }, workdir: p.workdir, slots: make(chan struct{}, 1)}
		if out, err := found.execute(context.Background(), commandInput{Script: "'hi'"}); err != nil || strings.TrimSpace(out.Stdout) != "hi" {
			t.Fatalf("resolved PowerShell failed: %+v, %v", out, err)
		}
	})
	for _, in := range []commandInput{{}, {Script: "hi", TimeoutSeconds: -1}, {Script: "hi", TimeoutSeconds: 121}, {Script: "hi", WorkingDirectory: "relative"}} {
		if _, err := p.execute(context.Background(), in); err == nil {
			t.Fatalf("accepted invalid input: %+v", in)
		}
	}
}

func TestLiveCloudflareTunnel(t *testing.T) {
	if os.Getenv("TEST_CLOUDFLARE") != "1" {
		t.Skip("set TEST_CLOUDFLARE=1 to test a real public tunnel")
	}
	binary, err := exec.LookPath("cloudflared")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(testToken, testShell(t)))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var logs bytes.Buffer
	tunnel, err := startTunnel(ctx, binary, server.URL, &logs)
	if err != nil {
		t.Log(logs.String())
		t.Fatal(err)
	}
	defer func() {
		tunnel.stop()
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	// Cloudflare prints its hostname before the edge connection is ready.
	deadline := time.Now().Add(75 * time.Second)
	for {
		_, err = rpcRequest(tunnel.url+"/mcp", "ping", map[string]any{})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	verifyMCP(t, tunnel.url+"/mcp")
}
