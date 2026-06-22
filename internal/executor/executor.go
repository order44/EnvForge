package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS == "windows" {
		return RunCommandContext(ctx, "cmd", "/C", command)
	}
	return RunCommandContext(ctx, "sh", "-c", command)
}

// RunShellRetry runs a shell command with retry on transient failures.
func RunShellRetry(ctx context.Context, cfg RetryConfig, command string) Result {
	if runtime.GOOS == "windows" {
		return RunCommandRetry(ctx, cfg, "cmd", "/C", command)
	}
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
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Env = nonInteractiveEnv()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func VSCodeExtensionInstalled(ctx context.Context, id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return false
	}

	cliCandidates := []string{"code", "code.cmd"}
	if runtime.GOOS == "windows" {
		cliCandidates = []string{}
		for _, p := range []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Microsoft VS Code", "bin", "code.cmd"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft VS Code", "bin", "code.cmd"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft VS Code", "bin", "code.cmd"),
			"code.cmd",
			"code",
		} {
			if p != "" {
				cliCandidates = append(cliCandidates, p)
			}
		}
	}

	for _, cli := range cliCandidates {
		for _, userName := range possibleUsersForChecks() {
			out, err := commandOutputAs(ctx, userName, cli, "--list-extensions")
			if err == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					if strings.EqualFold(strings.TrimSpace(line), id) {
						return true
					}
				}
			}
		}
	}

	for _, dir := range vscodeExtensionDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			if name == id || strings.HasPrefix(name, id+"-") {
				return true
			}
		}
	}

	return false
}

func PipxPackageInstalled(ctx context.Context, spec string) bool {
	pkgName := normalizePipxSpec(spec)
	if pkgName == "" {
		return false
	}

	for _, userName := range possibleUsersForChecks() {
		out, err := commandOutputAs(ctx, userName, "pipx", "list", "--json")
		if err != nil {
			continue
		}
		var body struct {
			Venvs map[string]json.RawMessage `json:"venvs"`
		}
		if err := json.Unmarshal(out, &body); err != nil {
			continue
		}
		for name := range body.Venvs {
			if strings.EqualFold(name, pkgName) {
				return true
			}
		}
	}

	return false
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
	env := append([]string{}, os.Environ()...)
	if runtime.GOOS != "windows" {
		env = setEnv(env, "DEBIAN_FRONTEND", "noninteractive")
		env = setEnv(env, "NEEDRESTART_MODE", "a")
		env = setEnv(env, "UCF_FORCE_CONFFOLD", "1")
		env = setEnv(env, "APT_LISTCHANGES_FRONTEND", "none")
	}

	realHome := realUserHome()
	if os.Getenv("SUDO_USER") != "" && realHome != "" {
		env = setEnv(env, "HOME", realHome)
	}
	env = setEnv(env, "PATH", augmentedPath(realHome))
	return env
}

func augmentedPath(realHome string) string {
	sep := string(os.PathListSeparator)
	seen := map[string]bool{}
	var parts []string

	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		parts = append(parts, p)
	}

	for _, p := range strings.Split(os.Getenv("PATH"), sep) {
		add(strings.TrimSpace(p))
	}

	if runtime.GOOS == "windows" {
		for _, p := range []string{
			filepath.Join(os.Getenv("SystemRoot"), "System32"),
			filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Microsoft VS Code", "bin"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft VS Code", "bin"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft VS Code", "bin"),
			filepath.Join(os.Getenv("ProgramFiles"), "nodejs"),
			filepath.Join(os.Getenv("APPDATA"), "npm"),
		} {
			add(p)
		}
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
			if base == "" {
				continue
			}
			for _, pat := range []string{
				filepath.Join(base, "Python*"),
				filepath.Join(base, "Programs", "Python", "Python*"),
			} {
				matches, _ := filepath.Glob(pat)
				for _, m := range matches {
					add(m)
					add(filepath.Join(m, "Scripts"))
				}
			}
		}
		return strings.Join(parts, sep)
	}

	for _, p := range []string{
		"/usr/local/bin",
		"/usr/local/sbin",
		"/usr/bin",
		"/usr/sbin",
		"/bin",
		"/sbin",
		"/usr/local",
		"/root/.local/bin",
		"/root/.cargo/bin",
	} {
		add(p)
	}

	if realHome != "" {
		for _, p := range []string{
			filepath.Join(realHome, ".local", "bin"),
			filepath.Join(realHome, ".cargo", "bin"),
			filepath.Join(realHome, ".pyenv", "bin"),
			filepath.Join(realHome, ".pyenv", "shims"),
			filepath.Join(realHome, ".local", "share", "pnpm"),
			filepath.Join(realHome, ".npm-global", "bin"),
		} {
			add(p)
		}

		nvmBins, _ := filepath.Glob(filepath.Join(realHome, ".nvm", "versions", "node", "*", "bin"))
		for _, p := range nvmBins {
			add(p)
		}
	}

	return strings.Join(parts, sep)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func realUserHome() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			return u.HomeDir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func possibleUsersForChecks() []string {
	users := []string{""}
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		users = append(users, sudoUser)
	}
	return users
}

func commandOutputAs(ctx context.Context, userName, name string, args ...string) ([]byte, error) {
	if userName == "" || runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = nonInteractiveEnv()
		return cmd.Output()
	}
	cmd := exec.CommandContext(ctx, "su", "-l", userName, "-c", joinCmd(name, args))
	cmd.Env = nonInteractiveEnv()
	return cmd.Output()
}

func vscodeExtensionDirs() []string {
	home := realUserHome()
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".vscode", "extensions"),
		filepath.Join(home, ".vscode-server", "extensions"),
	}
}

func normalizePipxSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	if i := strings.Index(spec, "["); i >= 0 {
		spec = spec[:i]
	}
	if i := strings.Index(spec, "=="); i >= 0 {
		spec = spec[:i]
	}
	if strings.HasPrefix(spec, "git+") {
		spec = strings.TrimPrefix(spec, "git+")
		spec = strings.TrimSuffix(spec, ".git")
		parts := strings.Split(strings.Trim(spec, "/"), "/")
		if len(parts) > 0 {
			spec = parts[len(parts)-1]
		}
	}
	return strings.ToLower(spec)
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
