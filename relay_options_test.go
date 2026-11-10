package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
)

func clearRelayEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PORT", "RELAY_PUBLIC_URL", "RAILWAY_PUBLIC_DOMAIN", "RAILWAY_VOLUME_MOUNT_PATH", "RAILWAY_SERVICE_ID", "RELAY_ENROLLMENT_KEY"} {
		t.Setenv(name, "")
	}
}

func TestRelayEnvironmentAndOverrides(t *testing.T) {
	clearRelayEnvironment(t)
	local, err := parseRelayOptions(nil)
	if err != nil || local.listen != "127.0.0.1:8788" || local.data != "relay-data/registry.json" {
		t.Fatalf("local defaults: %+v %v", local, err)
	}
	t.Setenv("PORT", "4321")
	t.Setenv("RAILWAY_SERVICE_ID", "test-service")
	t.Setenv("RAILWAY_PUBLIC_DOMAIN", "relay.up.railway.app")
	t.Setenv("RAILWAY_VOLUME_MOUNT_PATH", t.TempDir())
	options, err := parseRelayOptions(nil)
	if err != nil || options.listen != "0.0.0.0:4321" || options.publicURL != "https://relay.up.railway.app" || options.data != filepath.Join(os.Getenv("RAILWAY_VOLUME_MOUNT_PATH"), "registry.json") {
		t.Fatalf("Railway defaults: %+v %v", options, err)
	}
	t.Setenv("RELAY_PUBLIC_URL", "https://custom.example/")
	options, err = parseRelayOptions(nil)
	if err != nil || options.publicURL != "https://custom.example" {
		t.Fatalf("custom domain: %+v %v", options, err)
	}
	options, err = parseRelayOptions([]string{"-listen", "127.0.0.1:9876", "-public-url", "http://localhost:9876", "-data", "custom.json", "-max-devices", "25"})
	if err != nil || options.listen != "127.0.0.1:9876" || options.publicURL != "http://localhost:9876" || options.data != "custom.json" || options.maxDevices != 25 {
		t.Fatalf("flag overrides: %+v %v", options, err)
	}
}

func TestRelayRejectsIncompleteRailwaySetup(t *testing.T) {
	clearRelayEnvironment(t)
	t.Setenv("RAILWAY_SERVICE_ID", "test-service")
	t.Setenv("PORT", "4321")
	if _, err := parseRelayOptions(nil); err == nil || !strings.Contains(err.Error(), "volume") {
		t.Fatalf("missing volume: %v", err)
	}
	t.Setenv("RAILWAY_VOLUME_MOUNT_PATH", t.TempDir())
	if _, err := parseRelayOptions(nil); err == nil || !strings.Contains(err.Error(), "public domain") {
		t.Fatalf("missing domain: %v", err)
	}
	t.Setenv("RAILWAY_PUBLIC_DOMAIN", "relay.up.railway.app")
	for _, value := range []string{"invalid", "70000", "-1"} {
		t.Setenv("PORT", value)
		if _, err := parseRelayOptions(nil); err == nil {
			t.Fatalf("accepted invalid PORT %q", value)
		}
	}
	t.Setenv("PORT", "4321")
	t.Setenv("RELAY_PUBLIC_URL", "http://example.com")
	if _, err := parseRelayOptions(nil); err == nil {
		t.Fatal("accepted plaintext public relay")
	}
}

// Run the real relay entry point in a child with Railway's environment.
func TestRailwayRelayChild(t *testing.T) {
	if os.Getenv("TEST_RAILWAY_RELAY_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	if err := runRelay(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRailwayRelayStartupAndPersistence(t *testing.T) {
	clearRelayEnvironment(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	volume := t.TempDir()
	t.Setenv("TEST_RAILWAY_RELAY_CHILD", "1")
	t.Setenv("PORT", port)
	t.Setenv("RAILWAY_SERVICE_ID", "test-service")
	t.Setenv("RAILWAY_PUBLIC_DOMAIN", "relay.up.railway.app")
	t.Setenv("RAILWAY_VOLUME_MOUNT_PATH", volume)
	key := link.NewSecret()
	t.Setenv("RELAY_ENROLLMENT_KEY", key)
	endpoint := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 2 * time.Second}
	start := func() func() {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRailwayRelayChild$")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stop := func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
		t.Cleanup(stop)
		waitFor(t, func() bool {
			r, err := client.Get(endpoint + "/healthz")
			if err != nil {
				return false
			}
			defer r.Body.Close()
			body, _ := io.ReadAll(r.Body)
			return r.StatusCode == 200 && string(body) == "ok\n"
		}, "Railway relay health check")
		return stop
	}
	stop := start()
	id, secret := link.NewID(), link.NewSecret()
	register := func(password, enrollment string) int {
		body, _ := json.Marshal(link.Manifest{Name: "Railway test PC", Enabled: true, Routes: []link.Route{}})
		r, _ := http.NewRequest("PUT", endpoint+"/v1/devices/"+id, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+password)
		r.Header.Set("X-Enrollment-Key", enrollment)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if code := register(secret, ""); code != 401 {
		t.Fatalf("enrollment key ignored: %d", code)
	}
	if code := register(secret, key); code != 204 {
		t.Fatalf("registration: %d", code)
	}
	if _, err := os.Stat(filepath.Join(volume, "registry.json")); err != nil {
		t.Fatal("registry was not saved to the volume:", err)
	}
	stop()
	start()
	if code := register(link.NewSecret(), key); code != 401 {
		t.Fatalf("saved device identity lost: %d", code)
	}
	if code := register(secret, ""); code != 204 {
		t.Fatalf("existing device could not reconnect after restart: %d", code)
	}
}
