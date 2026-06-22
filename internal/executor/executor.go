package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/envforge/envforge/internal/logger"
)

// AptMutex prevents concurrent apt/dpkg invocations across the process.
var AptMutex sync.Mutex

// RetryableExitCodes lists exit codes that warrant a retry (typically network failures).
// curl exit 35 = SSL handshake error, 7 = connect failed, 6 = host resolve failed,
// 22 = http error, 28 = timeout, 56 = recv failure.
var RetryableExitCodes = map[int]bool{
	35: true, // SSL handshake
	7:  true, // connect failed
	6:  true, // DNS resolve
	28: true, // timeout
	56: true, // recv failure
	18: true, // partial file
	52: true, // empty reply from server
}

// RetryConfig controls retry behaviour for transient failures.
type RetryConfig struct {
	MaxAttempts int
	Backoff     time.Duration
}

// DefaultRetry is used for network-fetching commands.
var DefaultRetry = RetryConfig{MaxAttempts: 3, Backoff: 2 * time.Second}

type Result struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	Err      error
	Attempts int
}

func (r Result) IsSuccess() bool {
	return r.Err == nil && r.ExitCode == 0
}

type StreamLine struct {
	Text   string
	Stderr bool
}

func RunCommand(name string, args ...string) Result {
	return RunCommandContext(context.Background(), name, args...)
}

func RunCommandContext(ctx context.Context, name string, args ...string) Result {
	return runOnce(ctx, name, args, nil)
}

// RunCommandRetry executes a command, retrying on transient (network) failures.
func RunCommandRetry(ctx context.Context, cfg RetryConfig, name string, args ...string) Result {
	var last Result
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			last.Err = ctx.Err()
			return last
		}

		last = runOnce(ctx, name, args, nil)
		last.Attempts = attempt

		if last.IsSuccess() {
			return last
		}

		// only retry on known transient failures
		if !isTransient(last) {
			return last
		}

		if attempt < cfg.MaxAttempts {
			logger.Log.Warn().
				Int("attempt", attempt).
				Int("max", cfg.MaxAttempts).
				Int("exit_code", last.ExitCode).
				Str("cmd", last.Command).
				Msg("transient failure, retrying")

			select {
			case <-time.After(cfg.Backoff * time.Duration(attempt)):
			case <-ctx.Done():
				return last
			}
		}
	}

	return last
}

func RunShell(command string) Result {
	return RunShellContext(context.Background(), command)
}

func RunShellContext(ctx context.Context, command string) Result {
	return RunCommandContext(ctx, "sh", "-c", command)
}

// RunShellRetry runs a shell command with retry on transient failures.
func RunShellRetry(ctx context.Context, cfg RetryConfig, command string) Result {
	return RunCommandRetry(ctx, cfg, "sh", "-c", command)
}

func RunCommandStream(ctx context.Context, lines chan<- StreamLine, name string, args ...string) Result {
	if isAptLike(name) {
		AptMutex.Lock()
		defer AptMutex.Unlock()
	}
	defer close(lines)

	start := time.Now()
	cmdStr := joinCmd(name, args)
	logger.Log.Info().Str("cmd", cmdStr).Msg("executing (stream)")

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	cmd.Env = nonInteractiveEnv()

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	streamPipe := func(r io.Reader, buf *bytes.Buffer, isErr bool) {
		defer wg.Done()

		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024*64), 1024*1024)

		for scanner.Scan() {
			text := scanner.Text()

			buf.WriteString(text)
			buf.WriteByte('\n')

			select {
			case lines <- StreamLine{Text: text, Stderr: isErr}:
			case <-ctx.Done():
				return
			}
		}
	}

	if err := cmd.Start(); err != nil {
		return Result{Command: cmdStr, Err: err, Duration: time.Since(start)}
	}

	wg.Add(2)
	go streamPipe(stdoutPipe, &stdoutBuf, false)
	go streamPipe(stderrPipe, &stderrBuf, true)
	wg.Wait()

	err := cmd.Wait()
	duration := time.Since(start)

	result := Result{
		Command:  cmdStr,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		Duration: duration,
		Err:      err,
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	logResult(result)
	return result
}

func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func CheckShellSucceeds(ctx context.Context, command string) bool {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = nonInteractiveEnv()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// --- internal ---

func runOnce(ctx context.Context, name string, args []string, _ *RetryConfig) Result {
	if isAptLike(name) {
		AptMutex.Lock()
		defer AptMutex.Unlock()
	}

	start := time.Now()
	cmdStr := joinCmd(name, args)

	logger.Log.Info().Str("cmd", cmdStr).Msg("executing")

	cmd := exec.CommandContext(ctx, name, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	cmd.Env = nonInteractiveEnv()

	err := cmd.Run()
	duration := time.Since(start)

	result := Result{
		Command:  cmdStr,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
		Err:      err,
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	logResult(result)
	return result
}

// isTransient reports whether the error is worth retrying.
func isTransient(r Result) bool {
	if RetryableExitCodes[r.ExitCode] {
		return true
	}

	out := r.Stderr + r.Stdout
	for _, marker := range []string{
		"TLS connect error",
		"SSL connect error",
		"UNEXPECTED_EOF_WHILE_READING",
		"Connection reset by peer",
		"Could not resolve host",
		"Temporary failure in name resolution",
		"503 Service Unavailable",
		"502 Bad Gateway",
		"504 Gateway Timeout",
		"Connection timed out",
		"network is unreachable",
	} {
		if strings.Contains(out, marker) {
			return true
		}
	}

	return false
}

func nonInteractiveEnv() []string {
	return append(os.Environ(),
		"DEBIAN_FRONTEND=noninteractive",
		"NEEDRESTART_MODE=a",
		"UCF_FORCE_CONFFOLD=1",
		"APT_LISTCHANGES_FRONTEND=none",
	)
}

func isAptLike(name string) bool {
	switch name {
	case "apt", "apt-get", "aptitude", "dpkg":
		return true
	}
	return false
}

func joinCmd(name string, args []string) string {
	if len(args) == 0 {
		return shellQuote(name)
	}

	var b strings.Builder
	b.WriteString(shellQuote(name))

	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(a))
	}

	return b.String()
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}

	// Keep common safe command tokens readable in logs.
	if strings.IndexFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z':
			return false
		case r >= 'A' && r <= 'Z':
			return false
		case r >= '0' && r <= '9':
			return false
		}

		switch r {
		case '/', '.', '_', '-', '+', '=', ':', '@', '%', ',':
			return false
		}

		return true
	}) < 0 {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func logResult(r Result) {
	if r.Err != nil {
		logger.Log.Error().
			Str("cmd", r.Command).
			Str("stderr", truncate(r.Stderr, 800)).
			Int("exit_code", r.ExitCode).
			Dur("duration", r.Duration).
			Err(r.Err).
			Msg("command failed")
		return
	}

	logger.Log.Info().
		Str("cmd", r.Command).
		Int("exit_code", r.ExitCode).
		Dur("duration", r.Duration).
		Msg("command completed")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	return s[:max] + fmt.Sprintf("... (%d bytes truncated)", len(s)-max)
}
