package installer

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/envforge/envforge/internal/executor"
	"github.com/envforge/envforge/internal/manifest"
)

// getRealUser returns the original user when running under sudo.
func getRealUser() string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "root"
}

func runAsRealUser(ctx context.Context, cmd string) executor.Result {
	realUser := getRealUser()
	if realUser == "root" || runtime.GOOS == "windows" {
		return executor.RunShellContext(ctx, cmd)
	}
	wrapped := fmt.Sprintf("su -l %s -c '%s'", realUser, strings.ReplaceAll(cmd, "'", "'\\''"))
	return executor.RunShellContext(ctx, wrapped)
}

func pipxAvailable() bool {
	return executor.CommandExists("pipx")
}

func pipCommand() (name string, args []string) {
	if runtime.GOOS == "windows" {
		switch {
		case executor.CommandExists("py"):
			return "py", []string{"-m", "pip"}
		case executor.CommandExists("python"):
			return "python", []string{"-m", "pip"}
		case executor.CommandExists("pip"):
			return "pip", nil
		default:
			return "pip", nil
		}
	}

	switch {
	case executor.CommandExists("pip3"):
		return "pip3", nil
	case executor.CommandExists("python3"):
		return "python3", []string{"-m", "pip"}
	case executor.CommandExists("python"):
		return "python", []string{"-m", "pip"}
	default:
		return "pip3", nil
	}
}

func ensureLinuxPip(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if executor.CommandExists("pip3") || executor.CommandExists("python3") {
		return nil
	}
	res := executor.RunCommandContext(ctx,
		"apt-get",
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "Dpkg::Options::=--force-confdef",
		"install", "-y",
		"python3", "python3-pip",
	)
	if !res.IsSuccess() {
		return fmt.Errorf("failed to install python3/pip3: %s", nonEmpty(res.Stderr, res.Stdout))
	}
	return nil
}

func npmCommand() string {
	if executor.CommandExists("npm") {
		return "npm"
	}
	if runtime.GOOS == "windows" {
		const defaultNpm = `C:\Program Files\nodejs\npm.cmd`
		if _, err := os.Stat(defaultNpm); err == nil {
			return defaultNpm
		}
	}
	return "npm"
}

func ensureLinuxNpm(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if executor.CommandExists("npm") {
		return nil
	}
	res := executor.RunCommandContext(ctx,
		"apt-get",
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "Dpkg::Options::=--force-confdef",
		"install", "-y",
		"nodejs", "npm",
	)
	if !res.IsSuccess() {
		return fmt.Errorf("failed to install nodejs/npm: %s", nonEmpty(res.Stderr, res.Stdout))
	}
	return nil
}

func resolveVSCodeCLI() string {
	candidates := []string{"code", "code.cmd"}
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Microsoft VS Code", "bin", "code.cmd"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft VS Code", "bin", "code.cmd"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft VS Code", "bin", "code.cmd"),
		)
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, "\\") || strings.Contains(candidate, "/") {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
			continue
		}
		if executor.CommandExists(candidate) {
			return candidate
		}
	}
	return ""
}

func windowsQuote(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
}

var wingetSourcePrepareOnce sync.Once
var wingetSourcePrepareErr error

func prepareWingetSource(ctx context.Context) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	wingetSourcePrepareOnce.Do(func() {
		res := executor.RunCommandContext(ctx, "winget", "source", "update")
		if res.IsSuccess() {
			return
		}
		reset := executor.RunCommandContext(ctx, "winget", "source", "reset", "--force")
		if !reset.IsSuccess() {
			wingetSourcePrepareErr = fmt.Errorf("winget source update failed: %s; reset failed: %s", nonEmpty(res.Stderr, res.Stdout), nonEmpty(reset.Stderr, reset.Stdout))
			return
		}
		res = executor.RunCommandContext(ctx, "winget", "source", "update")
		if !res.IsSuccess() {
			wingetSourcePrepareErr = fmt.Errorf("winget source update failed after reset: %s", nonEmpty(res.Stderr, res.Stdout))
		}
	})
	return wingetSourcePrepareErr
}

// ---------------------------------------------------------------------------
// apt
// ---------------------------------------------------------------------------

type AptInstaller struct{}

func (a *AptInstaller) Name() string { return "apt" }

func (a *AptInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	if runtime.GOOS != "linux" {
		return InstallResult{Status: StatusSkipped, Error: "apt is linux-only", Duration: time.Since(start)}
	}
	if plat.RequiresAptUpdate {
		_ = executor.RunCommandContext(ctx, "apt-get", "update", "-qq")
	}
	args := append([]string{
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "Dpkg::Options::=--force-confdef",
		"install", "-y",
	}, plat.Packages...)
	r := executor.RunCommandContext(ctx, "apt-get", args...)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// winget
// ---------------------------------------------------------------------------

type WingetInstaller struct{}

func (w *WingetInstaller) Name() string { return "winget" }

func (w *WingetInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	if runtime.GOOS != "windows" {
		return InstallResult{Status: StatusSkipped, Error: "winget is windows-only", Duration: time.Since(start)}
	}
	if err := prepareWingetSource(ctx); err != nil {
		return InstallResult{Status: StatusFailed, Duration: time.Since(start), Error: err.Error()}
	}
	r := executor.RunCommandContext(ctx, "winget", "install", "-e",
		"--id", plat.ID,
		"--source", "winget",
		"--accept-package-agreements",
		"--accept-source-agreements",
		"--disable-interactivity",
		"--silent",
	)
	if !r.IsSuccess() {
		out := r.Stdout + r.Stderr
		if strings.Contains(out, "already installed") || strings.Contains(out, "No newer package") {
			return InstallResult{Status: StatusSkipped, Error: "already installed", Duration: time.Since(start), Output: r.Stdout}
		}
		if r.ExitCode == 2147954402 {
			return InstallResult{Status: StatusFailed, Error: "winget timeout (0x80072ee2): проверь интернет, VPN/Proxy, и сам winget source; также попробуй вручную `winget install --source winget --id " + plat.ID + " -e`", Duration: time.Since(start), Output: out}
		}
		if r.ExitCode == 2316632107 {
			return InstallResult{Status: StatusFailed, Error: "winget reported 'No applicable update found' (0x8A15002B). Обычно это поломанный/несинхронизированный source App Installer. Попробуй вручную: `winget source reset --force`, затем `winget source update`, затем `winget search --id " + plat.ID + " -e`", Duration: time.Since(start), Output: out}
		}
		if r.ExitCode == 2316632084 {
			return InstallResult{Status: StatusFailed, Error: "winget did not find the package (0x8A150014). Проверь package id и выполни вручную `winget search --id " + plat.ID + " -e`", Duration: time.Since(start), Output: out}
		}
		return InstallResult{Status: StatusFailed, Error: nonEmpty(r.Stderr, r.Stdout), Duration: time.Since(start), Output: r.Stdout}
	}
	return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r.Stdout}
}

// ---------------------------------------------------------------------------
// chocolatey (windows fallback)
// ---------------------------------------------------------------------------

type ChocoInstaller struct{}

func (c *ChocoInstaller) Name() string { return "choco" }

func (c *ChocoInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	if runtime.GOOS != "windows" {
		return InstallResult{Status: StatusSkipped, Error: "choco is windows-only", Duration: time.Since(start)}
	}
	r := executor.RunCommandContext(ctx, "choco", "install", "-y", plat.ID)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// snap
// ---------------------------------------------------------------------------

type SnapInstaller struct{}

func (s *SnapInstaller) Name() string { return "snap" }

func (s *SnapInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	if runtime.GOOS != "linux" {
		return InstallResult{Status: StatusSkipped, Error: "snap is linux-only", Duration: time.Since(start)}
	}
	args := []string{"install", plat.ID}
	if classic, ok := lookupBool(plat.Commands, "classic"); ok && classic {
		args = append(args, "--classic")
	}
	r := executor.RunCommandContext(ctx, "snap", args...)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// pip — with PEP 668 awareness and pipx fallback
// ---------------------------------------------------------------------------

type PipInstaller struct{}

func (p *PipInstaller) Name() string { return "pip" }

func (p *PipInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()

	if runtime.GOOS == "linux" {
		if err := ensureLinuxPip(ctx); err != nil {
			return InstallResult{Status: StatusFailed, Duration: time.Since(start), Error: err.Error()}
		}
	}

	name, prefix := pipCommand()
	args := append(append([]string{}, prefix...), "install")
	if runtime.GOOS != "windows" {
		args = append(args, "--break-system-packages")
	}
	args = append(args, plat.Packages...)

	r := executor.RunCommandRetry(ctx, executor.DefaultRetry, name, args...)
	if r.IsSuccess() {
		return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r.Stdout}
	}

	if runtime.GOOS != "windows" && pipxAvailable() && len(plat.Packages) > 0 {
		pipxArgs := append([]string{"install"}, plat.Packages...)
		r2 := executor.RunCommandRetry(ctx, executor.DefaultRetry, "pipx", pipxArgs...)
		if r2.IsSuccess() {
			return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r2.Stdout}
		}
		r.Stderr = "pip failed: " + r.Stderr + "\npipx failed: " + r2.Stderr
	}

	return InstallResult{Status: StatusFailed, Duration: time.Since(start), Output: r.Stdout, Error: nonEmpty(r.Stderr, r.Stdout)}
}

// ---------------------------------------------------------------------------
// pipx
// ---------------------------------------------------------------------------

type PipxInstaller struct{}

func (p *PipxInstaller) Name() string { return "pipx" }

func (p *PipxInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	if !pipxAvailable() {
		if runtime.GOOS != "linux" {
			return InstallResult{Status: StatusFailed, Duration: time.Since(start), Error: "pipx is not installed"}
		}
		inst := executor.RunCommandContext(ctx, "apt-get",
			"-o", "Dpkg::Options::=--force-confold",
			"-o", "Dpkg::Options::=--force-confdef",
			"install", "-y", "pipx")
		if !inst.IsSuccess() {
			return InstallResult{Status: StatusFailed, Duration: time.Since(start),
				Error: "pipx not installed and apt install pipx failed: " + nonEmpty(inst.Stderr, inst.Stdout)}
		}
	}

	cmd := fmt.Sprintf("pipx install %s", shellJoin(plat.Packages))
	if runtime.GOOS == "linux" {
		r := runAsRealUser(ctx, cmd)
		return resultFrom(r, start)
	}

	r := executor.RunShellRetry(ctx, executor.DefaultRetry, cmd)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// npm (global)
// ---------------------------------------------------------------------------

type NpmInstaller struct{}

func (n *NpmInstaller) Name() string { return "npm" }

func (n *NpmInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()

	if runtime.GOOS == "linux" {
		if err := ensureLinuxNpm(ctx); err != nil {
			return InstallResult{Status: StatusFailed, Duration: time.Since(start), Error: err.Error()}
		}
	}

	args := append([]string{"install", "-g"}, plat.Packages...)
	r := executor.RunCommandRetry(ctx, executor.DefaultRetry, npmCommand(), args...)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// go install
// ---------------------------------------------------------------------------

type GoInstaller struct{}

func (g *GoInstaller) Name() string { return "go" }

func (g *GoInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	goBin := "go"
	if runtime.GOOS == "linux" && !executor.CommandExists("go") {
		goBin = "/usr/local/go/bin/go"
	}
	cmd := fmt.Sprintf("GOBIN=/usr/local/bin %s install %s", goBin, plat.URL)
	r := executor.RunShellRetry(ctx, executor.DefaultRetry, cmd)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// cargo install
// ---------------------------------------------------------------------------

type CargoInstaller struct{}

func (c *CargoInstaller) Name() string { return "cargo" }

func (c *CargoInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	cmd := fmt.Sprintf(". $HOME/.cargo/env 2>/dev/null; cargo install %s", plat.ID)
	r := executor.RunShellRetry(ctx, executor.DefaultRetry, cmd)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// git clone (idempotent)
// ---------------------------------------------------------------------------

type GitInstaller struct{}

func (g *GitInstaller) Name() string { return "git" }

func (g *GitInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	dest := plat.InstallDir
	if dest == "" {
		if runtime.GOOS == "windows" {
			dest = `C:\Tools\` + pkg.ID
		} else {
			dest = "/opt/" + pkg.ID
		}
	}

	_ = executor.RunCommandContext(ctx, "git", "config", "--global", "--add", "safe.directory", dest)

	if dirIsGitRepo(dest) {
		r := executor.RunShellRetry(ctx, executor.DefaultRetry,
			fmt.Sprintf("cd %q && git pull --ff-only", dest))
		if r.IsSuccess() {
			return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: "already cloned; pulled updates\n" + r.Stdout}
		}
		return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: "already cloned (pull failed: " + r.Stderr + ")"}
	}

	r := executor.RunCommandRetry(ctx, executor.DefaultRetry, "git", "clone", "--depth=1", plat.URL, dest)
	if !r.IsSuccess() {
		return InstallResult{Status: StatusFailed, Duration: time.Since(start), Output: r.Stdout, Error: r.Stderr}
	}
	return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r.Stdout}
}

func dirIsGitRepo(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	g, err := os.Stat(path + "/.git")
	return err == nil && g.IsDir()
}

// ---------------------------------------------------------------------------
// script (curl | bash) with retry
// ---------------------------------------------------------------------------

type ScriptInstaller struct{}

func (s *ScriptInstaller) Name() string { return "script" }

func (s *ScriptInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	r := executor.RunShellRetry(ctx, executor.DefaultRetry, "curl -fsSL "+plat.URL+" | bash")
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// builtin
// ---------------------------------------------------------------------------

type BuiltinInstaller struct{}

func (b *BuiltinInstaller) Name() string { return "builtin" }

func (b *BuiltinInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	note := plat.Note
	if note == "" {
		note = "already provided by the OS"
	}
	return InstallResult{Status: StatusSkipped, Error: note}
}

// ---------------------------------------------------------------------------
// manual
// ---------------------------------------------------------------------------

type ManualInstaller struct{}

func (m *ManualInstaller) Name() string { return "manual" }

func (m *ManualInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	var outBuf strings.Builder

	for _, cmd := range plat.Commands {
		if ctx.Err() != nil {
			return InstallResult{Status: StatusFailed, Error: "cancelled", Duration: time.Since(start), Output: outBuf.String()}
		}
		r := executor.RunShellRetry(ctx, executor.DefaultRetry, cmd)
		outBuf.WriteString(r.Stdout)
		if !r.IsSuccess() {
			return InstallResult{Status: StatusFailed, Duration: time.Since(start), Output: outBuf.String(), Error: nonEmpty(r.Stderr, r.Stdout)}
		}
	}

	note := plat.Note
	if note == "" {
		note = "manual installation required"
	}
	if plat.URL != "" {
		note += "\n  see: " + plat.URL
	}
	if len(plat.Commands) > 0 {
		note = "manual step required after prerequisites were prepared\n\n" + note
	}
	return InstallResult{Status: StatusSkipped, Duration: time.Since(start), Output: outBuf.String(), Error: note}
}

// ---------------------------------------------------------------------------
// custom (with retry)
// ---------------------------------------------------------------------------

type CustomInstaller struct{}

func (c *CustomInstaller) Name() string { return "custom" }

func (c *CustomInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	var outBuf strings.Builder
	for _, cmd := range plat.Commands {
		if ctx.Err() != nil {
			return InstallResult{Status: StatusFailed, Error: "cancelled", Duration: time.Since(start), Output: outBuf.String()}
		}
		r := executor.RunShellRetry(ctx, executor.DefaultRetry, cmd)
		outBuf.WriteString(r.Stdout)
		if !r.IsSuccess() {
			return InstallResult{Status: StatusFailed, Duration: time.Since(start), Output: outBuf.String(), Error: nonEmpty(r.Stderr, r.Stdout)}
		}
	}
	return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: outBuf.String()}
}

// ---------------------------------------------------------------------------
// vscode_ext
// ---------------------------------------------------------------------------

type VscodeExtInstaller struct{}

func (v *VscodeExtInstaller) Name() string { return "vscode_ext" }

func (v *VscodeExtInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	cli := resolveVSCodeCLI()
	if cli == "" {
		return InstallResult{Status: StatusSkipped, Duration: time.Since(start), Error: "VS Code CLI not found (expected code/code.cmd). Install VS Code first or add its bin directory to PATH."}
	}

	var r executor.Result
	if runtime.GOOS == "windows" {
		r = executor.RunCommandContext(ctx, cli, "--install-extension", plat.ID, "--force")
	} else {
		cmd := fmt.Sprintf("%s --install-extension %s --force", cli, plat.ID)
		r = runAsRealUser(ctx, cmd)
	}
	if !r.IsSuccess() {
		return InstallResult{Status: StatusFailed, Duration: time.Since(start), Output: r.Stdout, Error: nonEmpty(r.Stderr, r.Stdout)}
	}
	return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r.Stdout}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func resultFrom(r executor.Result, start time.Time) InstallResult {
	if r.IsSuccess() {
		return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r.Stdout}
	}
	return InstallResult{Status: StatusFailed, Duration: time.Since(start), Output: r.Stdout, Error: nonEmpty(r.Stderr, r.Stdout)}
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func lookupBool(items []string, key string) (bool, bool) {
	for _, it := range items {
		if it == key+"=true" {
			return true, true
		}
		if it == key+"=false" {
			return false, true
		}
	}
	return false, false
}

func shellJoin(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, shellQuoteLite(it))
	}
	return strings.Join(quoted, " ")
}

func shellQuoteLite(s string) string {
	if s == "" {
		return "''"
	}
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
