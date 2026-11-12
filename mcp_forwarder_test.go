package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/persist"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A real stdio child exercises framing, process lifecycle, and HTTP forwarding.
// It never touches the desktop and does not require Cua to be installed.
func TestMCPForwarderChild(t *testing.T) {
	if os.Getenv("COMPUTER_ACCESS_MCP_TEST_CHILD") != "1" {
		return
	}
	_ = os.WriteFile("child-started", []byte("yes"), 0600)
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{Instructions: "Use observe to inspect the window."})
	schema := json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}`)
	server.AddTool(&mcp.Tool{Name: "observe", Description: "Observe pixels", InputSchema: schema, OutputSchema: json.RawMessage(`{"type":"object"}`), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte{137, 80, 78, 71}}, &mcp.TextContent{Text: string(req.Params.Arguments)}}, StructuredContent: map[string]any{"pid": os.Getpid()}, Meta: mcp.Meta{"fixture": true}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "block", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = os.WriteFile("started", []byte("yes"), 0600)
		<-ctx.Done()
		_ = os.WriteFile("canceled", []byte("yes"), 0600)
		return nil, ctx.Err()
	})
	server.AddTool(&mcp.Tool{Name: "error", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "expected tool failure"}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "protocol_error", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, &jsonrpc.Error{Code: -32602, Message: "expected protocol failure"}
	})
	server.AddTool(&mcp.Tool{Name: "exit", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = os.WriteFile("exited", []byte("yes"), 0600)
		os.Exit(0)
		return nil, nil
	})
	server.AddResource(&mcp.Resource{URI: "skill://fixture/SKILL.md", Name: "Skill", MIMEType: "text/markdown"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "skill://fixture/SKILL.md", MIMEType: "text/markdown", Text: "# Use observe\n"}}}, nil
	})
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "fixture://item/{id}", Name: "Item"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "template resource"}}}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0) // The Go test runner must not write PASS to the protocol stream.
}

func fixtureForwarder(t *testing.T) (*computerMCPHost, *stdioForwarder, string) {
	t.Helper()
	t.Setenv("COMPUTER_ACCESS_MCP_TEST_CHILD", "1")
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := newComputerMCPHost(testToken, &powerShell{workdir: dir, slots: make(chan struct{}, 4)})
	forwarder := newStdioForwarder(stdioConfig{Prefix: "cua_", Command: executable, Args: []string{"-test.run=^TestMCPForwarderChild$"}, Dir: dir}, func(extra string) *mcp.Server {
		return newMCPServer(host.shell, instructions+extra)
	})
	host.upstream = forwarder
	t.Cleanup(func() { forwarder.close() })
	return host, forwarder, dir
}

func TestMCPForwarding(t *testing.T) {
	host, _, _ := fixtureForwarder(t)
	server := httptest.NewServer(host)
	defer server.Close()
	endpoint := server.URL + "/mcp"
	init := rpc(t, endpoint, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if !strings.Contains(string(init["instructions"]), "prefix") || !strings.Contains(string(init["instructions"]), "Use observe") {
		t.Fatalf("upstream instructions missing: %s", init["instructions"])
	}
	catalog := rpc(t, endpoint, "tools/list", map[string]any{})
	var listed struct {
		Tools []*mcp.Tool `json:"tools"`
	}
	data, _ := json.Marshal(catalog)
	if err := json.Unmarshal(data, &listed); err != nil {
		t.Fatal(err)
	}
	var observe *mcp.Tool
	for _, tool := range listed.Tools {
		if tool.Name == "cua_observe" {
			observe = tool
		}
		if tool.Name == "observe" {
			t.Fatal("unprefixed tool exposed")
		}
	}
	if len(listed.Tools) != 6 || observe == nil || observe.Description != "Observe pixels" || !observe.Annotations.ReadOnlyHint || observe.OutputSchema == nil {
		t.Fatalf("catalog not preserved: %s", data)
	}
	if _, ok := observe.InputSchema.(map[string]any)["properties"].(map[string]any)["label"]; !ok {
		t.Fatal("input schema changed")
	}
	result := rpc(t, endpoint, "tools/call", map[string]any{"name": "cua_observe", "arguments": map[string]any{"label": "hello"}})
	if !strings.Contains(string(result["content"]), `"type":"image"`) || !strings.Contains(string(result["content"]), `iVBORw==`) || !strings.Contains(string(result["content"]), `hello`) || !strings.Contains(string(result["structuredContent"]), `"pid"`) || !strings.Contains(string(result["_meta"]), `fixture`) {
		t.Fatalf("result changed: %v", result)
	}
	failed := rpc(t, endpoint, "tools/call", map[string]any{"name": "cua_error", "arguments": map[string]any{}})
	if string(failed["isError"]) != "true" {
		t.Fatal("tool failure lost")
	}
	if _, err := rpcRequest(endpoint, "tools/call", map[string]any{"name": "cua_protocol_error", "arguments": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "-32602") {
		t.Fatalf("protocol error lost: %v", err)
	}
	resources := rpc(t, endpoint, "resources/list", map[string]any{})
	if !strings.Contains(string(resources["resources"]), "skill://fixture/SKILL.md") {
		t.Fatal("skill resource missing")
	}
	resource := rpc(t, endpoint, "resources/read", map[string]any{"uri": "skill://fixture/SKILL.md"})
	if !strings.Contains(string(resource["contents"]), `# Use observe\n`) {
		t.Fatalf("resource bytes changed: %s", resource["contents"])
	}
	templates := rpc(t, endpoint, "resources/templates/list", map[string]any{})
	if !strings.Contains(string(templates["resourceTemplates"]), "fixture://item/{id}") {
		t.Fatal("template missing")
	}
	resource = rpc(t, endpoint, "resources/read", map[string]any{"uri": "fixture://item/42"})
	if !strings.Contains(string(resource["contents"]), "template resource") {
		t.Fatal("template read failed")
	}
}

func TestMCPForwarderRestartAndCancellation(t *testing.T) {
	host, forwarder, dir := fixtureForwarder(t)
	server := httptest.NewServer(host)
	defer server.Close()
	endpoint := server.URL + "/mcp"
	before := rpc(t, endpoint, "tools/call", map[string]any{"name": "cua_observe", "arguments": map[string]any{}})
	if _, err := rpcRequest(endpoint, "tools/call", map[string]any{"name": "cua_exit", "arguments": map[string]any{}}); err == nil {
		t.Fatal("exit unexpectedly succeeded")
	}
	select {
	case <-forwarder.done:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit")
	}
	after := rpc(t, endpoint, "tools/call", map[string]any{"name": "cua_observe", "arguments": map[string]any{}})
	if bytes.Equal(before["structuredContent"], after["structuredContent"]) {
		t.Fatal("child was not restarted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cua_block","arguments":{}}}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, _ := http.DefaultClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
	}()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(dir, "started")); return err == nil }, "forwarded call start")
	cancel()
	<-done
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(dir, "canceled")); return err == nil }, "upstream cancellation")
	rpc(t, endpoint, "tools/call", map[string]any{"name": "cua_observe", "arguments": map[string]any{}})
}

func TestMCPForwarderSerializesCalls(t *testing.T) {
	host, _, dir := fixtureForwarder(t)
	server := httptest.NewServer(host)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cua_block","arguments":{}}}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	var calls sync.WaitGroup
	calls.Go(func() {
		response, _ := http.DefaultClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
	})
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(dir, "started")); return err == nil }, "blocked call")
	// Resource reads must remain available while desktop actions are queued.
	resource := rpc(t, server.URL+"/mcp", "resources/read", map[string]any{"uri": "skill://fixture/SKILL.md"})
	if !strings.Contains(string(resource["contents"]), "Use observe") {
		t.Fatal("resource read failed during desktop action")
	}
	observed := make(chan error, 1)
	calls.Go(func() {
		_, err := rpcRequest(server.URL+"/mcp", "tools/call", map[string]any{"name": "cua_observe", "arguments": map[string]any{}})
		observed <- err
	})
	select {
	case err := <-observed:
		t.Fatalf("concurrent action bypassed queue: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	calls.Wait()
}

func TestComputerUseUnavailableAndDisable(t *testing.T) {
	host := newComputerMCPHost(testToken, &powerShell{})
	defer host.close()
	host.configure(computerUseConfig{Enabled: true, Executable: filepath.Join(t.TempDir(), "missing-driver")})
	server := httptest.NewServer(host)
	defer server.Close()
	if _, err := rpcRequest(server.URL+"/mcp", "tools/list", map[string]any{}); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("missing driver: %v", err)
	}
	host.configure(computerUseConfig{})
	result := rpc(t, server.URL+"/mcp", "tools/list", map[string]any{})
	if strings.Contains(string(result["tools"]), "cua_") || !strings.Contains(string(result["tools"]), "powershell") {
		t.Fatal("disabling did not restore built-in tools")
	}
}

func TestComputerUseServiceLifecycle(t *testing.T) {
	host, forwarder, dir := fixtureForwarder(t)
	a, err := openAgent(filepath.Join(t.TempDir(), "agent.dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.builtinToken, a.builtinHandler, a.builtinHost = testToken, host, host
	// Retain the fixture launch command while exercising production configuration
	// and service lifecycle code.
	computer := computerUseConfig{Enabled: true, Executable: "fixture"}
	host.config = computer
	if err := a.mutate(func(c *agentConfig) error {
		c.ComputerUse = computer
		c.Services = append(c.Services, newService("Second assistant"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(a.directHandler))
	defer server.Close()
	first, second := a.snapshot().Services[0], a.snapshot().Services[1]
	endpoint := func(s serviceConfig) string { return server.URL + "/s/" + s.URLID }
	data := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cua_observe","arguments":{}}}`)
	if code, _, err := publicRequest(endpoint(first), "wrong", "POST", data); err != nil || code != 401 {
		t.Fatalf("authentication: %d %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "child-started")); !os.IsNotExist(err) {
		t.Fatal("unauthenticated request launched driver")
	}
	call := func(s serviceConfig) []byte {
		t.Helper()
		code, body, err := publicRequest(endpoint(s), s.Password, "POST", data)
		if err != nil || code != 200 {
			t.Fatalf("service call: %d %v", code, err)
		}
		var response struct {
			Result struct {
				StructuredContent json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		return response.Result.StructuredContent
	}
	if !bytes.Equal(call(first), call(second)) {
		t.Fatal("services launched separate driver processes")
	}
	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		_, _, _ = publicRequest(endpoint(first), first.Password, "POST", []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cua_block","arguments":{}}}`))
	}()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(dir, "started")); return err == nil }, "service action")
	if err := a.desktopServiceAction(first.ID, "password"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("credential rotation did not cancel call")
	}
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(dir, "canceled")); return err == nil }, "revoked upstream action")
	if code, _, err := publicRequest(endpoint(first), first.Password, "POST", data); err != nil || code != 401 {
		t.Fatalf("old token accepted: %d %v", code, err)
	}
	call(second)
	if err := a.mutate(func(c *agentConfig) error { c.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-forwarder.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping access did not stop driver")
	}
	var saved agentConfig
	if err := persist.Load(a.path, &saved, true); err != nil {
		t.Fatal(err)
	}
	if saved.ComputerUse != computer || saved.Services[0].ID != first.ID {
		t.Fatal("computer settings or service identity not persisted")
	}
}

func TestInstalledCuaDriver(t *testing.T) {
	if os.Getenv("TEST_CUA_DRIVER") != "1" {
		t.Skip("set TEST_CUA_DRIVER=1 for read-only installed-driver interoperability")
	}
	path, err := exec.LookPath("cua-driver")
	if err != nil {
		t.Fatal(err)
	}
	host := newComputerMCPHost(testToken, &powerShell{workdir: t.TempDir()})
	host.configure(computerUseConfig{Enabled: true, Executable: path})
	defer host.close()
	// Prepare directly so a protocol failure is visible, without printing desktop data.
	_, err = host.upstream.prepare(context.Background())
	if err != nil {
		t.Fatal(fmt.Errorf("installed driver discovery: %w", err))
	}
	server := httptest.NewServer(host)
	defer server.Close()
	result, err := rpcRequest(server.URL+"/mcp", "tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result["tools"]), "cua_get_window_state") {
		t.Fatal("Cua window observation tool missing")
	}
	result, err = rpcRequest(server.URL+"/mcp", "tools/call", map[string]any{"name": "cua_list_apps", "arguments": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(result["isError"]) == "true" {
		t.Fatal("installed driver returned a tool error for list_apps")
	}
}
