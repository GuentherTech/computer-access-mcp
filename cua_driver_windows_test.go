//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledCuaDriverWithStalePath(t *testing.T) {
	if os.Getenv("TEST_CUA_DRIVER") != "1" {
		t.Skip("set TEST_CUA_DRIVER=1 for installed-driver interoperability")
	}
	t.Setenv("PATH", t.TempDir())
	host := newComputerMCPHost(testToken, &powerShell{workdir: t.TempDir()})
	host.configure(computerUseConfig{Enabled: true})
	defer host.close()
	if _, err := host.upstream.prepare(context.Background()); err != nil {
		t.Fatalf("discover installed Cua with stale PATH: %v", err)
	}
}

func TestFindCuaDriverWithStalePath(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)
	t.Setenv("PATH", t.TempDir())
	installed := filepath.Join(localAppData, "Programs", "Cua", "cua-driver", "bin", "cua-driver.exe")
	if err := os.MkdirAll(filepath.Dir(installed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cua-driver", "cua-driver.exe", installed} {
		got, err := findCuaDriver(name)
		if err != nil || got != installed {
			t.Fatalf("find %q = %q, %v", name, got, err)
		}
	}
	// A supplied path must never silently select another installation.
	if _, err := findCuaDriver(filepath.Join(t.TempDir(), "cua-driver.exe")); err == nil {
		t.Fatal("missing explicit path fell back to the installed driver")
	}
	pathDriver := filepath.Join(t.TempDir(), "cua-driver.exe")
	if err := os.WriteFile(pathDriver, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(pathDriver))
	if got, err := findCuaDriver("cua-driver"); err != nil || got != pathDriver {
		t.Fatalf("PATH preference: %q, %v", got, err)
	}
}
