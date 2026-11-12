package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `You are connected to the user's local PC through computer-access-mcp.
Use the powershell tool for command-line operations on this PC, including reading, writing,
searching, listing, and editing files, running programs, inspecting processes,
system administration, and network requests originating on this PC. Use the
cua_ tools for graphical interaction when available. Use PowerShell cmdlets
such as Get-Content, Set-Content, Get-ChildItem, and Select-String for file access.
Every call starts a fresh PowerShell 7 process. Variables and the current directory
do not persist. Set working_directory explicitly or use absolute paths. The default
directory is the server's configured working folder. Commands run as the user who
started the server and have that account's permissions. There is no sandbox or
per-command approval prompt. Follow the user's intent and ask before destructive
or irreversible actions. Never print or transmit authentication secrets. Commands
are noninteractive. Use timeout_seconds up to 120 and keep output concise. For
long-running work, start a separate process and inspect its status in later calls.`

type requestContextKey struct{}

func newHandler(token string, shell *powerShell) http.Handler {
	return authenticateMCP(token, mcpTransport(newMCPServer(shell, instructions)))
}

func newMCPServer(shell *powerShell, serverInstructions string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "computer-access-mcp", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: serverInstructions})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "powershell",
		Description: "Run a noninteractive PowerShell 7 script on the user's PC for file reads, writes, searches, and process management. Each call starts a new process. Returns stdout, stderr, exit code, timeout, and truncation status.",
	}, shell.call)
	return server
}

func mcpTransport(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true})
}

func authenticateMCP(token string, next http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		header := r.Header.Get("Authorization")
		scheme, credential, ok := strings.Cut(header, " ")
		actual := sha256.Sum256([]byte(credential))
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="computer-access-mcp"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Browser clients are not needed. Reject cross-origin browser requests.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "Browser origins are not allowed", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		// Older MCP protocol versions detach tool contexts from the HTTP request.
		// Preserve the request context so disconnects and shutdown stop commands too.
		r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, r.Context()))
		next.ServeHTTP(w, r)
	})
}

func (p *powerShell) call(ctx context.Context, _ *mcp.CallToolRequest, input commandInput) (*mcp.CallToolResult, commandOutput, error) {
	ctx, cancel := toolRequestContext(ctx)
	defer cancel()
	out, err := p.execute(ctx, input)
	if err != nil {
		return nil, commandOutput{}, err
	}
	return &mcp.CallToolResult{IsError: out.ExitCode != 0 || out.TimedOut}, out, nil
}

func toolRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	if requestCtx, ok := ctx.Value(requestContextKey{}).(context.Context); ok {
		stop := context.AfterFunc(requestCtx, cancel)
		return ctx, func() { stop(); cancel() }
	}
	return ctx, cancel
}
