package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
	"github.com/GuentherTech/computer-access-mcp/internal/persist"
	"reflect"
)

func directAgent(t *testing.T) *agent {
	t.Helper()
	a, err := openAgent(filepath.Join(t.TempDir(), "agent.dat"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	a.builtinToken = link.NewSecret()
	a.builtinHandler = newHandler(a.builtinToken, testShell(t))
	return a
}

func TestDirectCloudflareGateway(t *testing.T) {
	a := directAgent(t)
	if a.snapshot().Mode != "cloudflare" {
		t.Fatal("new agents should default to Cloudflare")
	}
	server := httptest.NewServer(http.HandlerFunc(a.directHandler))
	defer server.Close()
	s := a.snapshot().Services[0]
	url := server.URL + "/s/" + s.URLID
	requireStatus(t, url, "wrong", 401)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"powershell","arguments":{"script":"Write-Output 'Direct Cloudflare gateway works'"}}}`)
	code, data, err := publicRequest(url, s.Password, "POST", body)
	if err != nil || code != 200 || !strings.Contains(string(data), "Direct Cloudflare gateway works") {
		t.Fatalf("direct MCP failed: %d %s %v", code, data, err)
	}
	oldPassword := s.Password
	if err := a.desktopServiceAction(s.ID, "password"); err != nil {
		t.Fatal(err)
	}
	s = a.snapshot().Services[0]
	requireStatus(t, url, oldPassword, 401)
	requireStatus(t, url, s.Password, 405)
	if err := a.desktopServiceAction(s.ID, "url"); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, url, s.Password, 404)
	s = a.snapshot().Services[0]
	url = server.URL + "/s/" + s.URLID
	requireStatus(t, url, s.Password, 405)
	if err := a.desktopServiceAction(s.ID, "power"); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, url, s.Password, 503)
	if err := a.desktopServiceAction(s.ID, "power"); err != nil {
		t.Fatal(err)
	}
	if err := a.mutate(func(c *agentConfig) error { c.Mode = "relay"; return nil }); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, url, s.Password, 503)
}

func TestCloudflareURLViewAndNoWebGUI(t *testing.T) {
	a := directAgent(t)
	c := a.snapshot()
	a.mu.Lock()
	a.publicURL = "https://first.trycloudflare.com"
	a.mu.Unlock()
	first := a.view().Services[0]
	if first.URL != "https://first.trycloudflare.com/s/"+c.Services[0].URLID {
		t.Fatal("missing Cloudflare service URL")
	}
	a.mu.Lock()
	a.publicURL = "https://second.trycloudflare.com"
	a.mu.Unlock()
	second := a.view().Services[0]
	if first.URL == second.URL || first.Password != second.Password {
		t.Fatal("reconnecting should change only the temporary base URL")
	}
	handler := a.uiHandler(c.ManagementToken, "127.0.0.1:17877")
	r := httptest.NewRequest("GET", "http://127.0.0.1:17877/", nil)
	r.Header.Set("Authorization", "Bearer "+c.ManagementToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("web management page is still exposed: %d", w.Code)
	}
	shown := false
	a.showGUI = func() { shown = true }
	r = httptest.NewRequest("POST", "http://127.0.0.1:17877/api/show", nil)
	r.Header.Set("Authorization", "Bearer "+c.ManagementToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 || !shown {
		t.Fatal("second-instance activation failed")
	}
}

func TestDesktopAddEditAndDelete(t *testing.T) {
	a := directAgent(t)
	if err := a.saveDesktopService("", "Other MCP"); err != nil {
		t.Fatal(err)
	}
	s := a.snapshot().Services[1]
	if err := a.saveDesktopService(s.ID, "Renamed MCP"); err != nil {
		t.Fatal(err)
	}
	updated := a.snapshot().Services[1]
	if updated.Password != s.Password || updated.URLID != s.URLID || updated.Name != "Renamed MCP" {
		t.Fatal("editing changed credentials or did not save name")
	}
	if err := a.desktopServiceAction(s.ID, "delete"); err != nil {
		t.Fatal(err)
	}
	if len(a.snapshot().Services) != 1 {
		t.Fatal("service was not removed")
	}
}

func TestRenameKeepsRunningCommands(t *testing.T) {
	a, err := openAgent(filepath.Join(t.TempDir(), "agent.dat"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	id := a.snapshot().Services[0].ID
	running := a.runtimes[id].ctx
	if err := a.saveDesktopService(id, "Renamed MCP"); err != nil {
		t.Fatal(err)
	}
	if running.Err() != nil {
		t.Fatal("renaming a service canceled its running commands")
	}
	if got := a.runtimes[id].config.Name; got != "Renamed MCP" {
		t.Fatalf("runtime kept stale name %q", got)
	}
	if err := a.desktopServiceAction(id, "password"); err != nil {
		t.Fatal(err)
	}
	if running.Err() == nil {
		t.Fatal("rotating the password did not cancel running commands")
	}
}

func TestDesktopRelayAddressIsFixed(t *testing.T) {
	a, err := openAgent(filepath.Join(t.TempDir(), "agent.dat"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	if err := a.saveDesktopConnection("relay", "PC", "https://relay.example.com", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.saveDesktopConnection("relay", "PC", "https://other.example.com", "", ""); err == nil {
		t.Fatal("desktop settings replaced the saved relay address")
	}
	if err := a.saveDesktopConnection("relay", "Renamed PC", "https://relay.example.com/", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.saveDesktopConnection("cloudflare", "Renamed PC", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if c := a.snapshot(); c.RelayURL != "https://relay.example.com" || c.Mode != "cloudflare" || c.Name != "Renamed PC" {
		t.Fatalf("unexpected saved connection: %+v", c)
	}
}

func TestLiveDirectCloudflare(t *testing.T) {
	if os.Getenv("TEST_CLOUDFLARE") != "1" {
		t.Skip("set TEST_CLOUDFLARE=1 for a real Cloudflare test")
	}
	a := directAgent(t)
	if err := a.mutate(func(c *agentConfig) error { c.Services[0].Password = testToken; return nil }); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(a.directHandler))
	defer server.Close()
	binary, err := findCloudflared("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tunnel, err := startTunnel(ctx, binary, server.URL, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.stop()
	url := tunnel.url + "/s/" + a.snapshot().Services[0].URLID
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, err := rpcRequest(url, "ping", map[string]any{})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	verifyMCP(t, url)
}

func TestRetiredProxyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.dat")
	a, err := openAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	before := a.snapshot().Services[0]
	saved := a.snapshot()
	proxy := newService("Old proxy")
	proxy.Kind = "http"
	saved.Services = append(saved.Services, proxy)
	a.close()
	if err := persist.Save(path, saved, true); err != nil {
		t.Fatal(err)
	}
	a, err = openAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	after := a.snapshot()
	a.close()
	if len(after.Services) != 1 || !reflect.DeepEqual(after.Services[0], before) {
		t.Fatal("migration did not remove only the proxy")
	}
	var disk agentConfig
	if err := persist.Load(path, &disk, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.Services, after.Services) {
		t.Fatal("migration was not persisted")
	}
}
