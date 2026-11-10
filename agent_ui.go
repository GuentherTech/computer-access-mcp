package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
)

type serviceView struct {
	serviceConfig
	URL string `json:"url"`
}
type agentView struct {
	Name      string        `json:"name"`
	RelayURL  string        `json:"relay_url"`
	Mode      string        `json:"mode"`
	Enabled   bool          `json:"enabled"`
	Connected bool          `json:"connected"`
	Error     string        `json:"error"`
	Services  []serviceView `json:"services"`
}

func (a *agent) view() agentView {
	a.mu.RLock()
	defer a.mu.RUnlock()
	v := agentView{Name: a.config.Name, RelayURL: a.config.RelayURL, Mode: a.config.Mode, Enabled: a.config.Enabled, Connected: a.connected, Error: a.lastError, Services: []serviceView{}}
	base := a.config.RelayURL
	if a.config.Mode == "cloudflare" {
		base = a.publicURL
	}
	for _, s := range a.config.Services {
		u := ""
		if base != "" {
			u = base + "/s/" + s.URLID
		}
		v.Services = append(v.Services, serviceView{serviceConfig: s, URL: u})
	}
	return v
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid request")
	}
	return nil
}

func (a *agent) uiHandler(token, host string) http.Handler {
	// Private loopback control API for second-instance activation and automation.
	// There is no web management page; the desktop UI calls the agent directly.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != host {
			http.Error(w, "Invalid host", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+host {
			http.Error(w, "Invalid origin", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !link.Match(link.Bearer(r), link.Hash(token)) {
				http.Error(w, "Open the management link printed by the agent to sign in.", 401)
				return
			}
			if r.Method == "GET" && r.URL.Path == "/api/state" {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(a.view())
				return
			}
			if r.Method == "POST" && r.URL.Path == "/api/shutdown" {
				w.WriteHeader(http.StatusNoContent)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				if a.quit != nil {
					a.quit()
				}
				return
			}
			if r.Method == "POST" && r.URL.Path == "/api/show" {
				a.mu.RLock()
				show := a.showGUI
				a.mu.RUnlock()
				if show != nil {
					show()
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if err := a.apiAction(w, r); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
}

func (a *agent) apiAction(w http.ResponseWriter, r *http.Request) error {
	if r.URL.Path == "/api/power" && r.Method == "POST" {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(w, r, &input); err != nil {
			return err
		}
		return a.mutate(func(c *agentConfig) error { c.Enabled = input.Enabled; return nil })
	}
	if r.URL.Path == "/api/settings" && r.Method == "POST" {
		var input struct {
			Name          string `json:"name"`
			RelayURL      string `json:"relay_url"`
			EnrollmentKey string `json:"enrollment_key"`
		}
		if err := readJSON(w, r, &input); err != nil {
			return err
		}
		base, err := link.BaseURL(input.RelayURL)
		if err != nil {
			return err
		}
		if strings.TrimSpace(input.Name) == "" || len(input.Name) > 100 {
			return errors.New("device name must contain 1 to 100 characters")
		}
		return a.mutate(func(c *agentConfig) error {
			if c.RelayURL != "" && c.RelayURL != base {
				return errors.New("the relay address is fixed to preserve your URLs")
			}
			c.RelayURL = base
			c.Name = strings.TrimSpace(input.Name)
			if input.EnrollmentKey != "" {
				c.EnrollmentKey = input.EnrollmentKey
			}
			return nil
		})
	}
	if r.URL.Path == "/api/services" && r.Method == "POST" {
		var input struct {
			Name string `json:"name"`
		}
		if err := readJSON(w, r, &input); err != nil {
			return err
		}
		return a.saveDesktopService("", input.Name)
	}
	if !strings.HasPrefix(r.URL.Path, "/api/services/") {
		return errors.New("unknown action")
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/services/"), "/")
	// Read request bodies before mutate takes the agent lock.
	var power struct {
		Enabled bool `json:"enabled"`
	}
	var rename struct {
		Name string `json:"name"`
	}
	if r.Method == "POST" && action == "power" {
		if err := readJSON(w, r, &power); err != nil {
			return err
		}
	}
	if r.Method == "PATCH" && action == "" {
		if err := readJSON(w, r, &rename); err != nil {
			return err
		}
	}
	return a.mutate(func(c *agentConfig) error {
		for i := range c.Services {
			s := &c.Services[i]
			if s.ID != id {
				continue
			}
			switch {
			case r.Method == "DELETE" && action == "":
				c.Services = append(c.Services[:i], c.Services[i+1:]...)
			case r.Method == "POST" && action == "url":
				s.URLID = link.NewID()
			case r.Method == "POST" && action == "password":
				s.Password = link.NewSecret()
			case r.Method == "POST" && action == "power":
				s.Enabled = power.Enabled
			case r.Method == "PATCH" && action == "":
				s.Name = strings.TrimSpace(rename.Name)
				if err := validateService(*s); err != nil {
					return err
				}
			default:
				return errors.New("unknown action")
			}
			return nil
		}
		return errors.New("service not found")
	})
}
