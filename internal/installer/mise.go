package installer

import (
	"context"
	"runtime"
	"time"

	"github.com/envforge/envforge/internal/executor"
	"github.com/envforge/envforge/internal/manifest"
)

// MiseInstaller installs mise (polyglot version manager)
type MiseInstaller struct{}

func (m *MiseInstaller) Name() string { return "mise" }

func (m *MiseInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()

	if runtime.GOOS != "linux" {
		return InstallResult{Status: StatusSkipped, Error: "mise installer is linux-only", Duration: time.Since(start)}
	}

	cmd := "curl -fsSL https://mise.run | sh"
	r := executor.RunShellRetry(ctx, executor.DefaultRetry, cmd)

	// Also try to install to /usr/local/bin if possible
	if r.IsSuccess() {
		executor.RunShellContext(ctx, "install -m 755 $HOME/.local/bin/mise /usr/local/bin/mise 2>/dev/null || true")
	}

	return resultFrom(r, start)
}
