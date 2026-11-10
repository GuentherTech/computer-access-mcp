package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

func relayClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (a *agent) register(ctx context.Context, c agentConfig) error {
	data, err := json.Marshal(manifest(c))
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, "PUT", c.RelayURL+"/v1/devices/"+c.DeviceID, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.DeviceSecret)
	r.Header.Set("Content-Type", "application/json")
	if c.EnrollmentKey != "" {
		r.Header.Set("X-Enrollment-Key", c.EnrollmentKey)
	}
	response, err := relayClient().Do(r)
	if err != nil {
		return errors.New("Cannot reach the relay. Check its address and your connection.")
	}
	defer response.Body.Close()
	if response.StatusCode != 204 {
		switch response.StatusCode {
		case 401:
			return errors.New("The relay rejected this device or its enrollment key.")
		case 409:
			return errors.New("A service URL is already assigned to another device. Regenerate that URL.")
		default:
			return fmt.Errorf("The relay could not save this device, HTTP %d.", response.StatusCode)
		}
	}
	return nil
}

func (a *agent) setStatus(connected bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = connected
	a.lastError = ""
	if err != nil {
		a.lastError = err.Error()
	}
}

func (a *agent) connectLoop(ctx context.Context) {
	defer a.setStatus(false, nil)
	delay := time.Second
	for ctx.Err() == nil {
		c := a.snapshot()
		if c.RelayURL == "" {
			a.setStatus(false, nil)
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
				continue
			}
		}
		err := a.register(ctx, c)
		if err == nil && !c.Enabled {
			a.setStatus(false, nil)
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
				continue
			}
		}
		if err == nil {
			err = a.connectOnce(ctx, c)
		}
		if ctx.Err() != nil {
			return
		}
		a.setStatus(false, err)
		if err == nil {
			delay = time.Second
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.wake:
			timer.Stop()
			delay = time.Second
		case <-timer.C:
			if delay < 15*time.Second {
				delay *= 2
				if delay > 15*time.Second {
					delay = 15 * time.Second
				}
			}
		}
	}
}

type muxListener struct{ *yamux.Session }

func (l muxListener) Addr() net.Addr { return l.Session.LocalAddr() }

func (a *agent) connectOnce(ctx context.Context, c agentConfig) error {
	u, _ := url.Parse(c.RelayURL + "/v1/agent")
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	ws, response, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.DeviceSecret}, "X-Device-ID": []string{c.DeviceID}}})
	dialCancel()
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return errors.New("The agent connection failed. Retrying automatically.")
	}
	defer ws.CloseNow()
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, err := yamux.Server(websocket.NetConn(connectionCtx, ws, websocket.MessageBinary), link.MuxConfig())
	if err != nil {
		return err
	}
	defer session.Close()
	server := &http.Server{Handler: http.HandlerFunc(a.serveService), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return connectionCtx }}
	server.SetKeepAlivesEnabled(false)
	done := make(chan error, 1)
	go func() { done <- server.Serve(muxListener{session}) }()
	defer server.Close()
	a.setStatus(true, nil)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-session.CloseChan():
			return errors.New("Relay connection lost. Reconnecting automatically.")
		case <-done:
			return errors.New("Relay connection closed. Reconnecting automatically.")
		case <-a.wake:
			latest := a.snapshot()
			if err := a.register(ctx, latest); err != nil {
				return err
			}
			if !latest.Enabled {
				return nil
			}
		}
	}
}

func (a *agent) serveService(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-Relay-Service-ID")
	a.mu.RLock()
	runtime, ok := a.runtimes[id]
	a.mu.RUnlock()
	if !ok || runtime.ctx.Err() != nil {
		http.Error(w, "Service is off", 503)
		return
	}
	s := runtime.config
	// Recheck both credentials locally. A stale relay route cannot use a
	// regenerated URL, a revoked password, or a deleted service.
	if r.Header.Get("X-Relay-URL-ID") != s.URLID {
		http.NotFound(w, r)
		return
	}
	if !link.Match(link.Bearer(r), link.Hash(s.Password)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="computer-access"`)
		http.Error(w, "Invalid password", 401)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(runtime.ctx, cancel)
	defer stop()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	r.Header.Del("X-Relay-Service-ID")
	r.Header.Del("X-Relay-URL-ID")
	if s.Kind != "powershell" {
		http.Error(w, "Unsupported service type", http.StatusGone)
		return
	}
	if a.builtinHandler == nil {
		http.Error(w, "PowerShell service is unavailable", 503)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	r.URL.Path = "/mcp"
	r.URL.RawPath = ""
	r.Host = "127.0.0.1"
	r.Header.Set("Authorization", "Bearer "+a.builtinToken)
	a.builtinHandler.ServeHTTP(w, r)
}
