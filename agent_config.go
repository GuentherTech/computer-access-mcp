package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
	"github.com/GuentherTech/computer-access-mcp/internal/persist"
)

type serviceConfig struct {
	ID       string `json:"id"`
	URLID    string `json:"url_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Password string `json:"password"`
	Enabled  bool   `json:"enabled"`
}

type agentConfig struct {
	Version         int               `json:"version"`
	DeviceID        string            `json:"device_id"`
	DeviceSecret    string            `json:"device_secret"`
	ManagementToken string            `json:"management_token"`
	Name            string            `json:"name"`
	Mode            string            `json:"mode,omitempty"`
	Cloudflared     string            `json:"cloudflared,omitempty"`
	RelayURL        string            `json:"relay_url"`
	EnrollmentKey   string            `json:"enrollment_key,omitempty"`
	Enabled         bool              `json:"enabled"`
	ComputerUse     computerUseConfig `json:"computer_use,omitempty"`
	Services        []serviceConfig   `json:"services"`
}

type serviceRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	config serviceConfig
}

type agent struct {
	mu             sync.RWMutex
	config         agentConfig
	path           string
	unlock         func()
	wake           chan struct{}
	runtimes       map[string]serviceRuntime
	connected      bool
	lastError      string
	quit           context.CancelFunc
	showGUI        func()
	publicURL      string
	builtinToken   string
	builtinHandler http.Handler
	builtinHost    *computerMCPHost
}

func defaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ComputerAccess", "agent.dat"), nil
}

func openAgent(path string) (*agent, error) {
	unlock, err := persist.Lock(path)
	if err != nil {
		return nil, err
	}
	a := &agent{path: path, unlock: unlock, wake: make(chan struct{}, 1), runtimes: map[string]serviceRuntime{}}
	err = persist.Load(path, &a.config, true)
	if errors.Is(err, os.ErrNotExist) {
		name, _ := os.Hostname()
		a.config = agentConfig{Version: 1, DeviceID: link.NewID(), DeviceSecret: link.NewSecret(), ManagementToken: link.NewSecret(), Name: name, Mode: "cloudflare", Enabled: true, Services: []serviceConfig{newService("PowerShell MCP")}}
		err = persist.Save(path, a.config, true)
	}
	if err != nil {
		unlock()
		return nil, err
	}
	if a.config.Version != 1 || !link.ValidID(a.config.DeviceID) || len(a.config.DeviceSecret) < 40 || len(a.config.ManagementToken) < 40 {
		unlock()
		return nil, errors.New("invalid saved agent configuration")
	}
	// Preserve existing relay configurations when upgrading older installations.
	if a.config.Mode == "" {
		if a.config.RelayURL != "" {
			a.config.Mode = "relay"
		} else {
			a.config.Mode = "cloudflare"
		}
	}
	if a.config.Mode != "relay" && a.config.Mode != "cloudflare" {
		unlock()
		return nil, errors.New("invalid connection mode")
	}
	if a.config.RelayURL != "" {
		if _, err := link.BaseURL(a.config.RelayURL); err != nil {
			unlock()
			return nil, err
		}
	}
	// Remove retired HTTP proxy services without changing PowerShell identities.
	services := make([]serviceConfig, 0, len(a.config.Services))
	removed := false
	for _, service := range a.config.Services {
		if service.Kind == "http" {
			removed = true
			continue
		}
		services = append(services, service)
	}
	if removed {
		a.config.Services = services
	}
	ids := map[string]bool{}
	urls := map[string]bool{}
	for _, s := range a.config.Services {
		if !link.ValidID(s.ID) || !link.ValidID(s.URLID) || len(s.Password) < 40 || ids[s.ID] || urls[s.URLID] {
			unlock()
			return nil, errors.New("invalid saved services")
		}
		if err := validateService(s); err != nil {
			unlock()
			return nil, err
		}
		ids[s.ID] = true
		urls[s.URLID] = true
	}
	if removed {
		if err := persist.Save(path, a.config, true); err != nil {
			unlock()
			return nil, err
		}
	}
	a.applyRuntimesLocked()
	return a, nil
}

func newService(name string) serviceConfig {
	return serviceConfig{ID: link.NewID(), URLID: link.NewID(), Name: strings.TrimSpace(name), Kind: "powershell", Password: link.NewSecret(), Enabled: true}
}

func validateService(s serviceConfig) error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 80 {
		return errors.New("service name must contain 1 to 80 characters")
	}
	if s.Kind != "powershell" {
		return errors.New("only built-in PowerShell MCP services are supported")
	}
	return nil
}

func (a *agent) snapshot() agentConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	c := a.config
	c.Services = append([]serviceConfig{}, c.Services...)
	return c
}

// mutate persists before publishing changes, so failed saves never change routes.
func (a *agent) mutate(fn func(*agentConfig) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.config
	c.Services = append([]serviceConfig{}, c.Services...)
	if err := fn(&c); err != nil {
		return err
	}
	if err := persist.Save(a.path, c, true); err != nil {
		return fmt.Errorf("save configuration: %w", err)
	}
	a.config = c
	a.applyRuntimesLocked()
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}

func (a *agent) applyRuntimesLocked() {
	active := map[string]bool{}
	for _, s := range a.config.Services {
		if !a.config.Enabled || !s.Enabled {
			continue
		}
		active[s.ID] = true
		old, ok := a.runtimes[s.ID]
		if ok && old.config.URLID == s.URLID && old.config.Password == s.Password && old.config.Kind == s.Kind {
			// Renames keep running commands. Credential changes cancel them below.
			old.config = s
			a.runtimes[s.ID] = old
			continue
		}
		if ok {
			old.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.runtimes[s.ID] = serviceRuntime{ctx: ctx, cancel: cancel, config: s}
	}
	for id, runtime := range a.runtimes {
		if !active[id] {
			runtime.cancel()
			delete(a.runtimes, id)
		}
	}
	if a.builtinHost != nil {
		computer := a.config.ComputerUse
		computer.Enabled = computer.Enabled && len(a.runtimes) > 0
		a.builtinHost.configure(computer)
	}
}

func (a *agent) close() {
	a.mu.Lock()
	for _, r := range a.runtimes {
		r.cancel()
	}
	a.runtimes = map[string]serviceRuntime{}
	a.mu.Unlock()
	if a.builtinHost != nil {
		a.builtinHost.close()
	}
	a.unlock()
}

func manifest(c agentConfig) link.Manifest {
	m := link.Manifest{Name: c.Name, Enabled: c.Enabled, Routes: []link.Route{}}
	for _, s := range c.Services {
		m.Routes = append(m.Routes, link.Route{ServiceID: s.ID, URLID: s.URLID, PasswordHash: link.Hash(s.Password), Enabled: s.Enabled})
	}
	return m
}
