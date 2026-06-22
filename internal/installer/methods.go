package installer

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
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

// pipxAvailable checks if pipx is installed on the system.
func pipxAvailable() bool {
	return executor.CommandExists("pipx")
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
	r := executor.RunCommandContext(ctx, "winget", "install", "-e",
		"--id", plat.ID,
		"--accept-package-agreements",
		"--accept-source-agreements",
		"--silent",
	)
	if !r.IsSuccess() {
		out := r.Stdout + r.Stderr
		if strings.Contains(out, "already installed") || strings.Contains(out, "No newer package") {
			return InstallResult{Status: StatusSkipped, Error: "already installed", Duration: time.Since(start), Output: r.Stdout}
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

	args := append([]string{"install", "--break-system-packages"}, plat.Packages...)
	r := executor.RunCommandRetry(ctx, executor.DefaultRetry, "pip3", args...)
	if r.IsSuccess() {
		return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r.Stdout}
	}

	if pipxAvailable() && len(plat.Packages) > 0 {
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
		inst := executor.RunCommandContext(ctx, "apt-get",
			"-o", "Dpkg::Options::=--force-confold",
			"install", "-y", "pipx")
		if !inst.IsSuccess() {
			return InstallResult{Status: StatusFailed, Duration: time.Since(start),
				Error: "pipx not installed and apt install pipx failed: " + inst.Stderr}
		}
	}
	args := append([]string{"install"}, plat.Packages...)
	r := executor.RunCommandRetry(ctx, executor.DefaultRetry, "pipx", args...)
	return resultFrom(r, start)
}

// ---------------------------------------------------------------------------
// npm (global)
// ---------------------------------------------------------------------------

type NpmInstaller struct{}

func (n *NpmInstaller) Name() string { return "npm" }

func (n *NpmInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	args := append([]string{"install", "-g"}, plat.Packages...)
	r := executor.RunCommandRetry(ctx, executor.DefaultRetry, "npm", args...)
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
			return InstallResult{Status: StatusSuccess, Duration: time.Since(start),
				Output: "already cloned; pulled updates\n" + r.Stdout}
		}
		return InstallResult{Status: StatusSuccess, Duration: time.Since(start),
			Output: "already cloned (pull failed: " + r.Stderr + ")"}
	}

	r := executor.RunCommandRetry(ctx, executor.DefaultRetry,
		"git", "clone", "--depth=1", plat.URL, dest)
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
	note := plat.Note
	if note == "" {
		note = "manual installation required"
	}
	if plat.URL != "" {
		note += "\n  see: " + plat.URL
	}
	return InstallResult{Status: StatusSkipped, Error: note}
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
			return InstallResult{
				Status:   StatusFailed,
				Duration: time.Since(start),
				Output:   outBuf.String(),
				Error:    nonEmpty(r.Stderr, r.Stdout),
			}
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
	cmd := fmt.Sprintf("code --install-extension %s --force", plat.ID)
	r := runAsRealUser(ctx, cmd)
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
