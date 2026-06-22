package installer

import (
	"context"
	"fmt"
	"time"

	"github.com/envforge/envforge/internal/executor"
	"github.com/envforge/envforge/internal/logger"
	"github.com/envforge/envforge/internal/manifest"
	"github.com/envforge/envforge/internal/platform"
)

type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusSuccess
	StatusFailed
	StatusSkipped
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusSuccess:
		return "ok"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// InstallResult is returned for every installation attempt.
type InstallResult struct {
	PackageID   string
	PackageName string
	Status      Status
	Duration    time.Duration
	Output      string
	Error       string
}

// Installer is the interface every installation method must implement.
type Installer interface {
	Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult
	Name() string
}

// Registry holds all available installers and dispatches by method name.
type Registry struct {
	installers map[string]Installer
	osInfo     platform.OSInfo
	dryRun     bool
}

// NewRegistry creates a Registry with all built-in installers registered.
func NewRegistry(osInfo platform.OSInfo) *Registry {
	r := &Registry{
		installers: make(map[string]Installer),
		osInfo:     osInfo,
	}
	r.Register(&AptInstaller{})
	r.Register(&WingetInstaller{})
	r.Register(&ChocoInstaller{})
	r.Register(&PipInstaller{})
	r.Register(&PipxInstaller{})
	r.Register(&NpmInstaller{})
	r.Register(&GoInstaller{})
	r.Register(&CargoInstaller{})
	r.Register(&GitInstaller{})
	r.Register(&ScriptInstaller{})
	r.Register(&BuiltinInstaller{})
	r.Register(&ManualInstaller{})
	r.Register(&CustomInstaller{})
	r.Register(&SnapInstaller{})
	r.Register(&VscodeExtInstaller{})
	r.Register(&WarpInstaller{})
	r.Register(&MiseInstaller{})
	return r
}

// SetDryRun toggles dry-run mode: no commands are executed, all installs return Skipped.
func (r *Registry) SetDryRun(v bool) { r.dryRun = v }

func (r *Registry) DryRun() bool { return r.dryRun }

func (r *Registry) Register(inst Installer) {
	r.installers[inst.Name()] = inst
}

func (r *Registry) Get(method string) (Installer, error) {
	inst, ok := r.installers[method]
	if !ok {
		return nil, fmt.Errorf("unknown install method: %s", method)
	}
	return inst, nil
}

// InstallPackage performs idempotency check, runs the installer, then post-install steps.
func (r *Registry) InstallPackage(ctx context.Context, pkg manifest.Package) InstallResult {
	osName := string(r.osInfo.OS)
	plat, ok := pkg.Platforms[osName]

	base := InstallResult{
		PackageID:   pkg.ID,
		PackageName: pkg.Name,
	}

	if !ok {
		base.Status = StatusSkipped
		base.Error = fmt.Sprintf("not supported on %s", osName)
		return base
	}

	// Idempotency check
	if plat.Check != "" {
		if executor.CheckShellSucceeds(ctx, plat.Check) {
			logger.Log.Info().Str("package", pkg.ID).Str("check", plat.Check).Msg("already installed, skipping")
			base.Status = StatusSkipped
			base.Error = "already installed"
			return base
		}
	}

	// Dry-run short-circuit
	if r.dryRun {
		base.Status = StatusSkipped
		base.Error = fmt.Sprintf("[dry-run] would install via %s", plat.Method)
		return base
	}

	// Context cancellation check before doing anything heavy
	if ctx.Err() != nil {
		base.Status = StatusFailed
		base.Error = "cancelled"
		return base
	}

	inst, err := r.Get(plat.Method)
	if err != nil {
		base.Status = StatusFailed
		base.Error = err.Error()
		return base
	}

	result := inst.Install(ctx, pkg, plat)
	result.PackageID = pkg.ID
	result.PackageName = pkg.Name

	// Post-install hooks
	if result.Status == StatusSuccess && len(plat.PostInstall) > 0 {
		for _, cmd := range plat.PostInstall {
			if ctx.Err() != nil {
				break
			}
			logger.Log.Info().
				Str("package", pkg.ID).
				Str("post_install", cmd).
				Msg("running post-install")
			res := executor.RunShellContext(ctx, cmd)
			if !res.IsSuccess() {
				logger.Log.Warn().
					Str("package", pkg.ID).
					Str("cmd", cmd).
					Str("stderr", res.Stderr).
					Msg("post-install command failed")
			}
		}
	}

	return result
}

// AptUpdate runs apt-get update once. Safe to call multiple times.
func (r *Registry) AptUpdate(ctx context.Context) error {
	if r.osInfo.OS != platform.Linux {
		return nil
	}
	if r.dryRun {
		return nil
	}
	logger.Log.Info().Msg("running apt-get update")
	res := executor.RunCommandContext(ctx, "apt-get", "update", "-qq")
	if !res.IsSuccess() {
		return fmt.Errorf("apt-get update failed: %s", res.Stderr)
	}
	return nil
}
