package installer

import (
	"context"
	"runtime"
	"time"

	"github.com/envforge/envforge/internal/executor"
	"github.com/envforge/envforge/internal/manifest"
)

// WarpInstaller installs Warp Terminal
type WarpInstaller struct{}

func (w *WarpInstaller) Name() string { return "warp" }

func (w *WarpInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()

	if runtime.GOOS != "linux" {
		return InstallResult{Status: StatusSkipped, Error: "warp is linux-only for now", Duration: time.Since(start)}
	}

	// Install .deb package
	r := executor.RunShellRetry(ctx, executor.DefaultRetry, "curl -fsSL https://app.warp.dev/download -o /tmp/warp.deb && (dpkg -i /tmp/warp.deb || apt-get install -f -y) && rm -f /tmp/warp.deb")

	return resultFrom(r, start)
}
