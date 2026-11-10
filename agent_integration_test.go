package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
	"github.com/GuentherTech/computer-access-mcp/internal/relay"
)

type relayFixture struct {
	agent        *agent
	relay        *relay.Server
	http         *httptest.Server
	registryPath string
	configPath   string
	cancel       context.CancelFunc
	done         chan struct{}
}

func newFixture(t *testing.T) *relayFixture {
	t.Helper()
	f := &relayFixture{registryPath: filepath.Join(t.TempDir(), "relay.json"), configPath: filepath.Join(t.TempDir(), "agent.dat")}
	var err error
	f.relay, err = relay.Open(f.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	f.http = httptest.NewServer(f.relay)
	f.agent, err = openAgent(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	f.agent.builtinToken = link.NewSecret()
	f.agent.builtinHandler = newHandler(f.agent.builtinToken, &powerShell{executable: "pwsh", workdir: t.TempDir(), slots: make(chan struct{}, 4)})
	if err := f.agent.mutate(func(c *agentConfig) error { c.Mode = "relay"; c.RelayURL = f.http.URL; return nil }); err != nil {
		t.Fatal(err)
	}
	f.startAgent()
	t.Cleanup(func() { f.stopAgent(); f.agent.close(); f.relay.Close(); f.http.Close() })
	waitFor(t, func() bool { return f.agent.view().Connected }, "agent connection")
	return f
}

func (f *relayFixture) startAgent() {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() { defer close(f.done); f.agent.connectLoop(ctx) }()
}
func (f *relayFixture) stopAgent() { f.cancel(); <-f.done }

func waitFor(t *testing.T, fn func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func publicRequest(endpoint, password, method string, data []byte) (int, []byte, error) {
	r, err := http.NewRequest(method, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Authorization", "Bearer "+password)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-06-18")
	c := &http.Client{Timeout: 10 * time.Second}
	response, err := c.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return response.StatusCode, body, err
}

func requireStatus(t *testing.T, endpoint, password string, expected int) string {
	t.Helper()
	status, body, err := publicRequest(endpoint, password, "GET", nil)
	if err != nil || status != expected {
		t.Fatalf("expected HTTP %d, got %d, body=%q, err=%v", expected, status, body, err)
	}
	return string(body)
}

func waitStatus(t *testing.T, endpoint, password string, expected int) {
	t.Helper()
	waitFor(t, func() bool {
		status, _, err := publicRequest(endpoint, password, "GET", nil)
		return err == nil && status == expected
	}, fmt.Sprintf("HTTP %d", expected))
}

func TestAgentRelayPowerShell(t *testing.T) {
	testShell(t)
	f := newFixture(t)
	s := f.agent.snapshot().Services[0]
	endpoint := f.http.URL + "/s/" + s.URLID
	data := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"integration","version":"1"}}}`)
	status, body, err := publicRequest(endpoint, s.Password, "POST", data)
	if err != nil || status != 200 || !strings.Contains(string(body), "ALL operations") {
		t.Fatalf("initialize failed: %d %s %v", status, body, err)
	}
	data = []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"powershell","arguments":{"script":"Write-Output 'Relay PowerShell works'"}}}`)
	status, body, err = publicRequest(endpoint, s.Password, "POST", data)
	if err != nil || status != 200 || !strings.Contains(string(body), "Relay PowerShell works") {
		t.Fatalf("PowerShell failed: %d %s %v", status, body, err)
	}
	requireStatus(t, endpoint, "wrong-password", 401)
}

func TestIndependentServicesAndRevocation(t *testing.T) {
	f := newFixture(t)
	f.agent.mu.Lock()
	f.agent.builtinHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "MCP handler") })
	f.agent.mu.Unlock()
	first := newService("First")
	second := newService("Second")
	if err := f.agent.mutate(func(c *agentConfig) error { c.Services = append(c.Services, first, second); return nil }); err != nil {
		t.Fatal(err)
	}
	firstURL := f.http.URL + "/s/" + first.URLID
	secondURL := f.http.URL + "/s/" + second.URLID
	waitStatus(t, firstURL, first.Password, 200)
	waitStatus(t, secondURL, second.Password, 200)
	requireStatus(t, firstURL+"/items", first.Password, 404)
	requireStatus(t, firstURL, second.Password, 401)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, err := publicRequest(secondURL, second.Password, "GET", nil)
			if err != nil || status != 200 {
				t.Errorf("concurrent request failed: %d %v", status, err)
			}
		}()
	}
	wg.Wait()
	oldPassword := first.Password
	if err := f.agent.mutate(func(c *agentConfig) error {
		for i := range c.Services {
			if c.Services[i].ID == first.ID {
				c.Services[i].Password = link.NewSecret()
				first = c.Services[i]
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Test the agent directly as if the relay still held the old password hash.
	requireAgentStatus(t, f.agent, first.ID, first.URLID, oldPassword, 401)
	waitStatus(t, firstURL, first.Password, 200)
	requireStatus(t, firstURL, oldPassword, 401)
	requireStatus(t, secondURL, second.Password, 200)
	oldID := first.URLID
	if err := f.agent.mutate(func(c *agentConfig) error {
		for i := range c.Services {
			if c.Services[i].ID == first.ID {
				c.Services[i].URLID = link.NewID()
				first = c.Services[i]
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireAgentStatus(t, f.agent, first.ID, oldID, first.Password, 404)
	firstURL = f.http.URL + "/s/" + first.URLID
	waitStatus(t, firstURL, first.Password, 200)
	waitStatus(t, f.http.URL+"/s/"+oldID, first.Password, 404)
	requireStatus(t, secondURL, second.Password, 200)
	if err := f.agent.mutate(func(c *agentConfig) error {
		for i := range c.Services {
			if c.Services[i].ID == first.ID {
				c.Services[i].Enabled = false
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireAgentStatus(t, f.agent, first.ID, first.URLID, first.Password, 503)
	waitStatus(t, firstURL, first.Password, 503)
	requireStatus(t, secondURL, second.Password, 200)
	if err := f.agent.mutate(func(c *agentConfig) error {
		for i := range c.Services {
			if c.Services[i].ID == first.ID {
				c.Services = append(c.Services[:i], c.Services[i+1:]...)
				break
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, firstURL, first.Password, 404)
	requireStatus(t, secondURL, second.Password, 200)
}

func requireAgentStatus(t *testing.T, a *agent, serviceID, urlID, password string, expected int) {
	t.Helper()
	r := httptest.NewRequest("GET", "http://agent/", nil)
	r.Header.Set("X-Relay-Service-ID", serviceID)
	r.Header.Set("X-Relay-URL-ID", urlID)
	r.Header.Set("Authorization", "Bearer "+password)
	w := httptest.NewRecorder()
	a.serveService(w, r)
	if w.Code != expected {
		t.Fatalf("agent returned %d, expected %d: %s", w.Code, expected, w.Body.String())
	}
}

func TestURLsSurviveAgentAndRelayRestart(t *testing.T) {
	f := newFixture(t)
	before := f.agent.snapshot()
	s := before.Services[0]
	// Restart the agent using its saved configuration.
	f.stopAgent()
	f.agent.close()
	var err error
	f.agent, err = openAgent(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	f.agent.builtinToken = link.NewSecret()
	f.agent.builtinHandler = newHandler(f.agent.builtinToken, &powerShell{executable: "pwsh", workdir: t.TempDir(), slots: make(chan struct{}, 4)})
	f.startAgent()
	waitFor(t, func() bool { return f.agent.view().Connected }, "agent reconnect")
	if !reflect.DeepEqual(before, f.agent.snapshot()) {
		t.Fatal("agent restart changed saved identities or credentials")
	}
	// Restart the relay on the same address and database.
	address := f.http.Listener.Addr().String()
	f.relay.Close()
	f.http.Close()
	f.relay, err = relay.Open(f.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	f.http = httptest.NewUnstartedServer(f.relay)
	f.http.Listener.Close()
	f.http.Listener = listener
	f.http.Start()
	waitStatus(t, f.http.URL+"/s/"+s.URLID, s.Password, 405)
	if !reflect.DeepEqual(before, f.agent.snapshot()) {
		t.Fatal("relay restart changed saved identities or credentials")
	}
	// Global off/on also preserves the URL and password.
	if err := f.agent.mutate(func(c *agentConfig) error { c.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !f.agent.view().Connected }, "agent off")
	requireStatus(t, f.http.URL+"/s/"+s.URLID, s.Password, 503)
	if err := f.agent.mutate(func(c *agentConfig) error { c.Enabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, f.http.URL+"/s/"+s.URLID, s.Password, 405)
	if !reflect.DeepEqual(before, f.agent.snapshot()) {
		t.Fatal("on/off changed saved identities or credentials")
	}
}

func TestRelayStreamsAndCancels(t *testing.T) {
	f := newFixture(t)
	canceled := make(chan struct{}, 1)
	f.agent.mu.Lock()
	f.agent.builtinHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		canceled <- struct{}{}
	})
	f.agent.mu.Unlock()
	s := newService("Streaming")
	if err := f.agent.mutate(func(c *agentConfig) error { c.Services = append(c.Services, s); return nil }); err != nil {
		t.Fatal(err)
	}
	// Wait for the route without starting the streaming handler.
	waitFor(t, func() bool {
		code, _, err := publicRequest(f.http.URL+"/s/"+s.URLID, "invalid", "GET", nil)
		return err == nil && code == 401
	}, "streaming route")
	r, _ := http.NewRequest("GET", f.http.URL+"/s/"+s.URLID, nil)
	r.Header.Set("Authorization", "Bearer "+s.Password)
	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		response.Body.Close()
		t.Fatalf("stream was buffered: %q %v", line, err)
	}
	response.Body.Close()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not cancel the local request")
	}
}

func TestSavedCredentialsAndManagementAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.dat")
	a, err := openAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if duplicate, err := openAgent(path); err == nil {
		duplicate.close()
		t.Fatal("two agents acquired the same config")
	}
	c := a.snapshot()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && (!bytes.HasPrefix(data, []byte("CAMCP-DPAPI")) || bytes.Contains(data, []byte(c.DeviceSecret)) || bytes.Contains(data, []byte(c.Services[0].Password))) {
		t.Fatal("Windows config contains plaintext credentials")
	}
	handler := a.uiHandler(c.ManagementToken, "127.0.0.1:17877")
	for _, tc := range []struct {
		token, host, origin string
		want                int
	}{
		{"", "127.0.0.1:17877", "", 401}, {c.ManagementToken, "evil.example", "", 403}, {c.ManagementToken, "127.0.0.1:17877", "https://evil.example", 403}, {c.ManagementToken, "127.0.0.1:17877", "http://127.0.0.1:17877", 200},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/state", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("management auth returned %d, want %d", w.Code, tc.want)
		}
		if w.Code == 200 && (strings.Contains(w.Body.String(), c.DeviceSecret) || strings.Contains(w.Body.String(), c.ManagementToken)) {
			t.Fatal("management view leaks agent credentials")
		}
	}
	before := a.snapshot()
	oldPath := a.path
	a.path = filepath.Join(path, "invalid")
	err = a.mutate(func(c *agentConfig) error { c.Services[0].Password = link.NewSecret(); return nil })
	a.path = oldPath
	if err == nil || !reflect.DeepEqual(before, a.snapshot()) {
		t.Fatal("failed save changed live configuration")
	}
}

func TestProxyServicesRejected(t *testing.T) {
	s := newService("Test")
	s.Kind = "http"
	if err := validateService(s); err == nil {
		t.Fatal("accepted retired HTTP service")
	}
	a, err := openAgent(filepath.Join(t.TempDir(), "agent.dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	handler := a.uiHandler(a.snapshot().ManagementToken, "127.0.0.1:17877")
	for _, body := range []string{`{"name":"Proxy","kind":"http","target":"http://127.0.0.1:9000"}`, `{"name":"Proxy","target":"http://127.0.0.1:9000"}`} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:17877/api/services", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+a.snapshot().ManagementToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("proxy configuration returned %d", w.Code)
		}
	}
}

func TestManagementServiceUpdates(t *testing.T) {
	a, err := openAgent(filepath.Join(t.TempDir(), "agent.dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	handler := a.uiHandler(a.snapshot().ManagementToken, "127.0.0.1:17877")
	id := a.snapshot().Services[0].ID
	send := func(method, path, body string) int {
		r := httptest.NewRequest(method, "http://127.0.0.1:17877"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+a.snapshot().ManagementToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"PATCH", "/api/services/" + id, `{"name":"Renamed"}`, 204},
		{"POST", "/api/services/" + id + "/power", `{"enabled":false}`, 204},
		{"POST", "/api/services/" + id + "/power", `{"enabled":true,"name":"x"}`, 400},
		{"PATCH", "/api/services/" + id, `{"name":"x","enabled":false}`, 400},
		{"PATCH", "/api/services/" + id, `{"name":" "}`, 400},
		{"PATCH", "/api/services/" + link.NewID(), `{"name":"Missing"}`, 400},
	} {
		if code := send(tc.method, tc.path, tc.body); code != tc.code {
			t.Fatalf("%s %s %s returned %d, want %d", tc.method, tc.path, tc.body, code, tc.code)
		}
	}
	if s := a.snapshot().Services[0]; s.Name != "Renamed" || s.Enabled {
		t.Fatalf("management updates not saved: %+v", s)
	}
}

func TestRestrictedEnrollment(t *testing.T) {
	r, err := relay.Open(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.EnrollmentKey = link.NewSecret()
	server := httptest.NewServer(r)
	defer server.Close()
	id, secret := link.NewID(), link.NewSecret()
	put := func(key string) int {
		body, _ := json.Marshal(link.Manifest{Name: "PC", Enabled: true, Routes: []link.Route{}})
		req, _ := http.NewRequest("PUT", server.URL+"/v1/devices/"+id, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("X-Enrollment-Key", key)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := put(""); status != 401 {
		t.Fatalf("unkeyed enrollment returned %d", status)
	}
	if status := put(r.EnrollmentKey); status != 204 {
		t.Fatalf("keyed enrollment returned %d", status)
	}
	if status := put(""); status != 204 {
		t.Fatalf("existing device required enrollment again: %d", status)
	}
}

func TestRelayPreventsDeviceAndURLTakeover(t *testing.T) {
	f := newFixture(t)
	c := f.agent.snapshot()
	put := func(id, secret string, m link.Manifest) int {
		body, _ := json.Marshal(m)
		r, _ := http.NewRequest("PUT", f.http.URL+"/v1/devices/"+id, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if code := put(c.DeviceID, link.NewSecret(), manifest(c)); code != 401 {
		t.Fatalf("device takeover returned %d", code)
	}
	if code := put(link.NewID(), link.NewSecret(), manifest(c)); code != 409 {
		t.Fatalf("URL takeover returned %d", code)
	}
	duplicate := manifest(c)
	duplicate.Routes = append(duplicate.Routes, duplicate.Routes[0])
	if code := put(c.DeviceID, c.DeviceSecret, duplicate); code != 400 {
		t.Fatalf("duplicate route returned %d", code)
	}
}

func TestDevicesRemainIndependent(t *testing.T) {
	f := newFixture(t)
	other, err := openAgent(filepath.Join(t.TempDir(), "other-agent.dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.close()
	other.builtinToken = link.NewSecret()
	other.builtinHandler = newHandler(other.builtinToken, &powerShell{executable: "pwsh", workdir: t.TempDir(), slots: make(chan struct{}, 4)})
	if err := other.mutate(func(c *agentConfig) error { c.Mode = "relay"; c.RelayURL = f.http.URL; return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); other.connectLoop(ctx) }()
	defer func() { cancel(); <-done }()
	first := f.agent.snapshot().Services[0]
	second := other.snapshot().Services[0]
	firstURL := f.http.URL + "/s/" + first.URLID
	secondURL := f.http.URL + "/s/" + second.URLID
	waitStatus(t, secondURL, second.Password, 405)
	requireStatus(t, firstURL, second.Password, 401)
	requireStatus(t, secondURL, first.Password, 401)
	if err := other.mutate(func(c *agentConfig) error { c.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, secondURL, second.Password, 503)
	requireStatus(t, firstURL, first.Password, 405)
}
