package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
	"github.com/GuentherTech/computer-access-mcp/internal/persist"
	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

type Device struct {
	SecretHash string        `json:"secret_hash"`
	Manifest   link.Manifest `json:"manifest"`
}
type database struct {
	Devices map[string]Device `json:"devices"`
}
type routeRef struct {
	deviceID string
	route    link.Route
	enabled  bool
}

type Server struct {
	mu            sync.RWMutex
	path          string
	db            database
	routes        map[string]routeRef
	sessions      map[string]*yamux.Session
	closing       bool
	unlock        func()
	EnrollmentKey string
	MaxDevices    int
	slots         chan struct{}
	deviceSlots   map[string]chan struct{}
}

func Open(path string) (*Server, error) {
	unlock, err := persist.Lock(path)
	if err != nil {
		return nil, err
	}
	s := &Server{path: path, db: database{Devices: map[string]Device{}}, routes: map[string]routeRef{}, sessions: map[string]*yamux.Session{}, unlock: unlock, MaxDevices: 1000, slots: make(chan struct{}, 256), deviceSlots: map[string]chan struct{}{}}
	if err := persist.Load(path, &s.db, false); err != nil && !errors.Is(err, os.ErrNotExist) {
		unlock()
		return nil, fmt.Errorf("load relay database: %w", err)
	}
	if s.db.Devices == nil {
		unlock()
		return nil, errors.New("invalid relay database")
	}
	for id, d := range s.db.Devices {
		s.index(id, d.Manifest)
	}
	return s, nil
}

func (s *Server) Close() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.closing = true
	for _, session := range s.sessions {
		session.Close()
	}
	s.mu.Unlock()
	s.unlock()
}

func (s *Server) index(id string, m link.Manifest) {
	for _, r := range m.Routes {
		s.routes[r.URLID] = routeRef{deviceID: id, route: r, enabled: m.Enabled}
	}
	if s.deviceSlots[id] == nil {
		s.deviceSlots[id] = make(chan struct{}, 16)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		w.Write([]byte("ok\n"))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/devices/") {
		s.register(w, r)
		return
	}
	if r.URL.Path == "/v1/agent" {
		s.connect(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/s/") {
		s.forward(w, r)
		return
	}
	http.NotFound(w, r)
}

func deny(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="computer-access"`)
	http.Error(w, "Invalid password", 401)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		w.Header().Set("Allow", "PUT")
		http.Error(w, "Method not allowed", 405)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "Browser origins are not allowed", 403)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/devices/")
	secret := link.Bearer(r)
	if !link.ValidID(id) || len(secret) < 40 || len(secret) > 256 {
		deny(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var manifest link.Manifest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || len(manifest.Name) > 100 || len(manifest.Routes) > 32 {
		http.Error(w, "Invalid device manifest; maximum 32 services", 400)
		return
	}
	seen := map[string]bool{}
	services := map[string]bool{}
	for _, route := range manifest.Routes {
		if !link.ValidID(route.ServiceID) || !link.ValidID(route.URLID) || len(route.PasswordHash) != 64 || seen[route.URLID] || services[route.ServiceID] {
			http.Error(w, "Invalid or duplicate service", 400)
			return
		}
		seen[route.URLID] = true
		services[route.ServiceID] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		http.Error(w, "Relay is stopping", 503)
		return
	}
	previous, exists := s.db.Devices[id]
	if exists {
		if !link.Match(secret, previous.SecretHash) {
			deny(w)
			return
		}
	} else {
		if s.EnrollmentKey != "" && !link.Match(r.Header.Get("X-Enrollment-Key"), link.Hash(s.EnrollmentKey)) {
			deny(w)
			return
		}
		if len(s.db.Devices) >= s.MaxDevices {
			http.Error(w, "Relay device limit reached", 503)
			return
		}
	}
	for _, route := range manifest.Routes {
		if ref, ok := s.routes[route.URLID]; ok && ref.deviceID != id {
			http.Error(w, "URL already assigned", 409)
			return
		}
	}
	s.db.Devices[id] = Device{SecretHash: link.Hash(secret), Manifest: manifest}
	if err := persist.Save(s.path, s.db, false); err != nil {
		if exists {
			s.db.Devices[id] = previous
		} else {
			delete(s.db.Devices, id)
		}
		http.Error(w, "Unable to save device", 500)
		return
	}
	for route, ref := range s.routes {
		if ref.deviceID == id {
			delete(s.routes, route)
		}
	}
	s.index(id, manifest)
	if !manifest.Enabled {
		if session := s.sessions[id]; session != nil {
			session.Close()
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.Header.Get("Origin") != "" {
		http.Error(w, "Not allowed", 403)
		return
	}
	id := r.Header.Get("X-Device-ID")
	s.mu.RLock()
	device, ok := s.db.Devices[id]
	closing := s.closing
	s.mu.RUnlock()
	if !ok || !link.Match(link.Bearer(r), device.SecretHash) {
		deny(w)
		return
	}
	if closing || !device.Manifest.Enabled {
		http.Error(w, "Device is off", 503)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(-1)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	session, err := yamux.Client(websocket.NetConn(ctx, ws, websocket.MessageBinary), link.MuxConfig())
	if err != nil {
		return
	}
	defer session.Close()
	s.mu.Lock()
	// Recheck after the WebSocket upgrade, which can race with a device update.
	if s.closing || !s.db.Devices[id].Manifest.Enabled {
		s.mu.Unlock()
		return
	}
	if previous := s.sessions[id]; previous != nil {
		previous.Close()
	}
	s.sessions[id] = session
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.sessions[id] == session {
			delete(s.sessions, id)
		}
		s.mu.Unlock()
	}()
	select {
	case <-session.CloseChan():
	case <-ctx.Done():
	}
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	urlID, suffix, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/s/"), "/")
	if !link.ValidID(urlID) {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	ref, ok := s.routes[urlID]
	session := s.sessions[ref.deviceID]
	deviceSlots := s.deviceSlots[ref.deviceID]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !link.Match(link.Bearer(r), ref.route.PasswordHash) {
		deny(w)
		return
	}
	if !ref.enabled || !ref.route.Enabled || session == nil || session.IsClosed() {
		http.Error(w, "Service is offline", 503)
		return
	}
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "WebSocket services are not supported", 501)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "Relay is busy", 503)
		return
	}
	select {
	case deviceSlots <- struct{}{}:
		defer func() { <-deviceSlots }()
	default:
		http.Error(w, "Device is busy", 503)
		return
	}
	// URLs carry service identities only. Passwords stay in Authorization headers.
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "agent"
			pr.Out.Host = "agent"
			pr.Out.URL.Path = "/" + suffix
			pr.Out.URL.RawPath = ""
			pr.Out.Header.Set("X-Relay-Service-ID", ref.route.ServiceID)
			pr.Out.Header.Set("X-Relay-URL-ID", urlID)
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
		},
		Transport: link.Transport{Session: session}, FlushInterval: -1,
		ErrorLog: log.New(os.Stderr, "relay: ", log.LstdFlags),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Device connection interrupted", 502)
		},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 130*time.Second)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
