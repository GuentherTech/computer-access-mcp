package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

type commandInput struct {
	Script           string `json:"script" jsonschema:"PowerShell script to run on the local PC"`
	WorkingDirectory string `json:"working_directory,omitempty" jsonschema:"Absolute working directory; defaults to the server working folder"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty" jsonschema:"Timeout in seconds from 1 to 120; default 60"`
}

type commandOutput struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`
}

type powerShell struct {
	executable string
	// lookup, when set, resolves the executable on each call so PowerShell
	// installed after the agent starts is found without a restart.
	lookup  func() (string, error)
	workdir string
	slots   chan struct{}
}

// A full pipe is still drained after the limit, so verbose commands cannot
// exhaust memory or block waiting for their stdout to be consumed.
type cappedBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

const outputLimit = 128 << 10

// errCommandTimeout separates timeout_seconds from caller cancellation and
// caller deadlines, which are not reported as timeouts.
var errCommandTimeout = errors.New("command timed out")

func (b *cappedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remaining := outputLimit - len(b.data)
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, data...)
	return n, nil
}

func (p *powerShell) execute(ctx context.Context, in commandInput) (commandOutput, error) {
	if strings.TrimSpace(in.Script) == "" {
		return commandOutput{}, errors.New("script is required")
	}
	if len(in.Script) > 24<<10 {
		return commandOutput{}, errors.New("script exceeds 24 KiB; write a script file in smaller calls and invoke it")
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 60
	}
	if in.TimeoutSeconds < 1 || in.TimeoutSeconds > 120 {
		return commandOutput{}, errors.New("timeout_seconds must be between 1 and 120")
	}
	dir := p.workdir
	if in.WorkingDirectory != "" {
		if !filepath.IsAbs(in.WorkingDirectory) {
			return commandOutput{}, errors.New("working_directory must be absolute")
		}
		dir = in.WorkingDirectory
	}
	executable := p.executable
	if p.lookup != nil {
		var err error
		if executable, err = p.lookup(); err != nil {
			return commandOutput{}, errors.New("PowerShell 7 is not installed on this PC")
		}
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return commandOutput{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeoutCause(ctx, time.Duration(in.TimeoutSeconds)*time.Second, errCommandTimeout)
	defer cancel()
	// Read the script from stdin rather than the Windows command line, which has
	// a 32767-character limit. Base64 safely quotes arbitrary user script text.
	userScript := base64.StdEncoding.EncodeToString([]byte(in.Script))
	wrapper := `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = [Console]::OutputEncoding
try {
    $script = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + userScript + `'))
    & ([ScriptBlock]::Create($script))
    if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} catch {
    [Console]::Error.WriteLine(($_ | Out-String))
    exit 1
}
`
	// A short encoded launcher avoids interactive stdin parsing of multi-line code.
	launcher := `& ([ScriptBlock]::Create([Console]::In.ReadToEnd()))`
	units := utf16.Encode([]rune(launcher))
	encoded := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(encoded[i*2:], unit)
	}
	cmd := exec.CommandContext(ctx, executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(wrapper)
	// Avoid passing a configured authentication token to ordinary child commands.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "COMPUTER_ACCESS_TOKEN=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	var stdout, stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	configureProcess(cmd)
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Run()
	// A background process started with -NoNewWindow inherits the output pipes.
	// The script itself exited successfully, so return the output read so far.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	out := commandOutput{Stdout: strings.ToValidUTF8(string(stdout.data), "�"), Stderr: strings.ToValidUTF8(string(stderr.data), "�"), Truncated: stdout.truncated || stderr.truncated, DurationMS: time.Since(start).Milliseconds(), TimedOut: context.Cause(ctx) == errCommandTimeout}
	if err != nil {
		out.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			out.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() == nil {
			return commandOutput{}, fmt.Errorf("run PowerShell: %w", err)
		}
	}
	return out, nil
}
