package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/envforge/envforge/internal/executor"
	"github.com/envforge/envforge/internal/manifest"
)

type VisualStudioInstaller struct{}

func (v *VisualStudioInstaller) Name() string { return "visual_studio" }

func (v *VisualStudioInstaller) Install(ctx context.Context, pkg manifest.Package, plat manifest.Platform) InstallResult {
	start := time.Now()
	if runtime.GOOS != "windows" {
		return InstallResult{Status: StatusSkipped, Error: "visual studio installer is windows-only", Duration: time.Since(start)}
	}
	if err := prepareWingetSource(ctx); err != nil {
		return InstallResult{Status: StatusFailed, Duration: time.Since(start), Error: err.Error()}
	}

	configPath, cleanup, err := resolveOrBuildVSConfig(plat)
	if err != nil {
		return InstallResult{Status: StatusFailed, Error: err.Error(), Duration: time.Since(start)}
	}
	if cleanup {
		defer os.Remove(configPath)
	}

	override := fmt.Sprintf("--quiet --wait --norestart --config %s", windowsQuote(configPath))
	r := executor.RunCommandContext(ctx,
		"winget", "install", "-e",
		"--id", plat.ID,
		"--source", "winget",
		"--accept-package-agreements",
		"--accept-source-agreements",
		"--disable-interactivity",
		"--override", override,
	)
	if !r.IsSuccess() {
		out := r.Stdout + r.Stderr
		if strings.Contains(out, "already installed") || strings.Contains(out, "No newer package") {
			return InstallResult{Status: StatusSkipped, Error: "already installed", Duration: time.Since(start), Output: out}
		}
		if r.ExitCode == 2147954402 {
			return InstallResult{Status: StatusFailed, Error: "winget timeout (0x80072ee2) while installing Visual Studio: проверь интернет/source winget и попробуй вручную `winget install --id " + plat.ID + " --override \"" + override + "\"`", Duration: time.Since(start), Output: out}
		}
		return InstallResult{Status: StatusFailed, Error: nonEmpty(r.Stderr, r.Stdout), Duration: time.Since(start), Output: out}
	}

	return InstallResult{Status: StatusSuccess, Duration: time.Since(start), Output: r.Stdout}
}

type vsConfig struct {
	Version    string   `json:"version"`
	Components []string `json:"components"`
}

func resolveOrBuildVSConfig(plat manifest.Platform) (path string, cleanup bool, err error) {
	if plat.URL != "" {
		if resolved := resolveLocalPath(plat.URL); resolved != "" {
			return resolved, false, nil
		}
	}
	if len(plat.Packages) == 0 {
		return "", false, fmt.Errorf("visual studio install requires either a .vsconfig file or package workload ids")
	}
	return buildTempVSConfig(plat.Packages)
}

func resolveLocalPath(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}

	candidates := []string{ref}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, ref),
			filepath.Join(exeDir, "..", ref),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, ref))
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}
	return ""
}

func buildTempVSConfig(components []string) (string, bool, error) {
	cfg := vsConfig{Version: "1.0", Components: components}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", false, fmt.Errorf("marshal vsconfig: %w", err)
	}
	f, err := os.CreateTemp("", "envforge-visualstudio-*.vsconfig")
	if err != nil {
		return "", false, fmt.Errorf("create temp vsconfig: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", false, fmt.Errorf("write temp vsconfig: %w", err)
	}
	return f.Name(), true, nil
}
