// Package link implements HTTP streams over the agent's single outbound socket.
package link

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func NewID() string            { return strings.ToLower(hex.EncodeToString(randomBytes(16))) }
func NewSecret() string        { return rand.Text() + rand.Text() }
func randomBytes(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }
func ValidID(id string) bool   { return idPattern.MatchString(id) }
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
func Match(secret, hash string) bool {
	actual := Hash(secret)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(hash)) == 1
}
func Bearer(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}

type Route struct {
	ServiceID    string `json:"service_id"`
	URLID        string `json:"url_id"`
	PasswordHash string `json:"password_hash"`
	Enabled      bool   `json:"enabled"`
}

type Manifest struct {
	Name    string  `json:"name"`
	Enabled bool    `json:"enabled"`
	Routes  []Route `json:"routes"`
}

func BaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u == nil {
		return "", errors.New("invalid relay URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Host == "" {
		return "", errors.New("relay URL must contain only a scheme and hostname, with an optional port")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && IsLoopback(u.Hostname())) {
		return "", errors.New("relay URL requires HTTPS, except on localhost")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func IsLoopback(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

func MuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.KeepAliveInterval = 15 * time.Second
	c.ConnectionWriteTimeout = 10 * time.Second
	c.StreamOpenTimeout = 10 * time.Second
	c.StreamCloseTimeout = 5 * time.Second
	c.AcceptBacklog = 32
	return c
}

type streamBody struct {
	io.ReadCloser
	stream net.Conn
	stop   func() bool
}

func (b *streamBody) Close() error { b.stop(); b.stream.Close(); return b.ReadCloser.Close() }

// Transport dedicates one multiplexed stream to each proxied HTTP request.
type Transport struct{ Session *yamux.Session }

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	stream, err := t.Session.OpenStream()
	if err != nil {
		// RoundTrippers must close the request body, including on errors.
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, err
	}
	stop := context.AfterFunc(r.Context(), func() { stream.Close() })
	if err = r.Write(stream); err != nil {
		stop()
		stream.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		stop()
		stream.Close()
		return nil, err
	}
	response.Body = &streamBody{ReadCloser: response.Body, stream: stream, stop: stop}
	return response, nil
}
