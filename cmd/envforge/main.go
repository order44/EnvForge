package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/envforge/envforge/internal/installer"
	"github.com/envforge/envforge/internal/logger"
	"github.com/envforge/envforge/internal/manifest"
	"github.com/envforge/envforge/internal/platform"
	"github.com/envforge/envforge/internal/preflight"
	"github.com/envforge/envforge/internal/profile"
	"github.com/envforge/envforge/internal/tui"
)

var Version = "0.3.0-dev"

func main() {
	var (
		versionFlag      = flag.Bool("version", false, "show version and exit")
		listFlag         = flag.Bool("list", false, "list all categories and packages")
		manifestInfoFlag = flag.Bool("manifest-info", false, "show which manifests were loaded and from where")
		listProfilesFlag = flag.Bool("list-profiles", false, "list saved profiles in the default profile directory")
		dryRun           = flag.Bool("dry-run", false, "go through everything but don't actually install")
		applyProfile     = flag.String("apply", "", "apply a profile non-interactively (name or path)")
		saveProfile      = flag.String("save", "", "save selection to profile after install (name or path)")
		loadProfile      = flag.String("load", "", "load a profile into TUI as initial selection")
		yes              = flag.Bool("yes", false, "skip confirmations for --apply")
		manifestDir      = flag.String("manifests", "", "override manifests directory (default: ./manifests next to binary)")
		logDir           = flag.String("logs", "", "override log directory (default: XDG state dir)")
	)
	flag.Parse()

	if *versionFlag {
		fmt.Printf("envforge v%s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	if *listProfilesFlag {
		printProfiles()
		os.Exit(0)
	}

	// privileges are not required for read-only commands
	requirePriv := !*listFlag && !*manifestInfoFlag && !*dryRun
	if requirePriv {
		requirePrivileges()
	}

	osInfo := platform.Detect()
	fmt.Printf("envforge v%s\n", Version)
	fmt.Printf("platform: %s\n", osInfo.String())

	if !osInfo.IsSupported() {
		fmt.Fprintf(os.Stderr, "error: unsupported OS: %s %s\n", osInfo.OS, osInfo.Distro)
		fmt.Fprintf(os.Stderr, "supported: debian, ubuntu, kali, mint, pop (linux); windows 10/11\n")
		os.Exit(1)
	}

	// logger
	if err := logger.Init(*logDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to init logger: %v\n", err)
	}
	defer logger.Close()
	logger.Log.Info().
		Str("version", Version).
		Str("os", string(osInfo.OS)).
		Str("distro", osInfo.Distro).
		Str("arch", osInfo.Arch).
		Bool("dry_run", *dryRun).
		Msg("envforge started")

	// resolve manifests directory
	resolvedMfDir := resolveManifestDir(*manifestDir)
	loadRes, err := manifest.LoadAllWithSources(resolvedMfDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load manifests: %v\n", err)
		os.Exit(1)
	}

	categories := manifest.FilterByPlatform(loadRes.Categories, string(osInfo.OS))
	logger.Log.Info().
		Int("categories", len(categories)).
		Int("embedded", loadRes.EmbedCount).
		Int("override", loadRes.OverrideCount).
		Msg("manifests loaded")

	if *manifestInfoFlag {
		printManifestInfo(loadRes, categories)
		os.Exit(0)
	}

	if *listFlag {
		printList(categories)
		os.Exit(0)
	}

	registry := installer.NewRegistry(osInfo)
	registry.SetDryRun(*dryRun)

	// --- non-interactive apply ---
	if *applyProfile != "" {
		runApply(*applyProfile, categories, registry, osInfo, *yes)
		return
	}

	// --- preflight checks for interactive TUI ---
	fmt.Println("\npreflight: checking internet, disk and installed tools...")
	preflightCtx, cancelPreflight := context.WithTimeout(context.Background(), 90*time.Second)
	preflightResult := preflight.Run(preflightCtx, categories, string(osInfo.OS), defaultDiskPath())
	cancelPreflight()

	installedCount := 0
	knownSizeCount := 0
	for _, st := range preflightResult.Packages {
		if st.Installed {
			installedCount++
		}
		if st.SizeKnown {
			knownSizeCount++
		}
	}

	if preflightResult.Internet.OK {
		fmt.Println("preflight: internet ok")
	} else {
		fmt.Printf("preflight: internet warning: %s\n", preflightResult.Internet.Error)
	}
	if preflightResult.Disk.Error == "" {
		fmt.Printf("preflight: disk free: %s\n", preflight.FormatBytes(preflightResult.Disk.FreeBytes))
	} else {
		fmt.Printf("preflight: disk warning: %s\n", preflightResult.Disk.Error)
	}
	fmt.Printf("preflight: installed tools detected: %d, package sizes known: %d/%d\n",
		installedCount, knownSizeCount, len(preflightResult.Packages))

	logger.Log.Info().
		Bool("internet", preflightResult.Internet.OK).
		Uint64("disk_free_bytes", preflightResult.Disk.FreeBytes).
		Int("installed", installedCount).
		Int("sizes_known", knownSizeCount).
		Int("packages", len(preflightResult.Packages)).
		Msg("preflight completed")

	// --- interactive TUI ---
	model := tui.NewModel(categories, osInfo, registry, preflightResult.Packages, preflightResult.Internet, preflightResult.Disk)

	if *loadProfile != "" {
		p, err := profile.Load(profile.ResolveProfilePath(*loadProfile))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: load profile: %v\n", err)
			os.Exit(1)
		}
		model.ApplyProfile(p.ToMap())
		fmt.Printf("loaded profile %q (%d packages)\n", p.Name, len(p.Selected))
	}

	// pre-flight apt update (so newly added repos in custom commands resolve)
	if osInfo.OS == platform.Linux && !*dryRun {
		_ = registry.AptUpdate(context.Background())
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// optional save after TUI exits
	if *saveProfile != "" {
		savePath := profile.ResolveProfilePath(*saveProfile)
		prof := profile.FromMap(filepath.Base(*saveProfile), string(osInfo.OS), model.Selection())
		if err := prof.Save(savePath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: save profile: %v\n", err)
		} else {
			fmt.Printf("profile saved: %s\n", savePath)
		}
	}

	fmt.Printf("\nlog: %s\n", logger.LogPath())
}

// runApply executes a profile non-interactively.
func runApply(profileRef string, categories []manifest.Category, reg *installer.Registry, osInfo platform.OSInfo, yes bool) {
	p, err := profile.Load(profile.ResolveProfilePath(profileRef))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Validate keys
	missing := []string{}
	queue := []manifest.Package{}
	for _, key := range p.Selected {
		_, _, pkg, ok := manifest.FindPackage(categories, key)
		if !ok {
			missing = append(missing, key)
			continue
		}
		queue = append(queue, *pkg)
	}

	fmt.Printf("\nprofile: %s\n", p.Name)
	fmt.Printf("packages to install: %d\n", len(queue))
	if len(missing) > 0 {
		fmt.Printf("not found in current manifests: %d\n", len(missing))
		for _, m := range missing {
			fmt.Printf("  - %s\n", m)
		}
	}

	if !yes {
		fmt.Print("\nproceed? [y/N]: ")
		var answer string
		fmt.Scanln(&answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			fmt.Println("aborted.")
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if osInfo.OS == platform.Linux {
		_ = reg.AptUpdate(ctx)
	}

	ok, fail, skip := 0, 0, 0
	start := time.Now()
	for i, pkg := range queue {
		fmt.Printf("[%d/%d] %s ... ", i+1, len(queue), pkg.Name)
		r := reg.InstallPackage(ctx, pkg)
		switch r.Status {
		case installer.StatusSuccess:
			fmt.Printf("ok (%s)\n", r.Duration.Round(100*time.Millisecond))
			ok++
		case installer.StatusFailed:
			fmt.Printf("FAILED: %s\n", firstLine(r.Error))
			fail++
		case installer.StatusSkipped:
			fmt.Printf("skipped (%s)\n", r.Error)
			skip++
		}
	}

	fmt.Printf("\ndone in %s — ok:%d  failed:%d  skipped:%d\n",
		time.Since(start).Round(time.Second), ok, fail, skip)
	fmt.Printf("log: %s\n", logger.LogPath())

	if fail > 0 {
		os.Exit(2)
	}
}

func requirePrivileges() {
	if runtime.GOOS == "linux" {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "error: envforge must be run as root")
			fmt.Fprintln(os.Stderr, "  run: sudo ./envforge")
			os.Exit(1)
		}
	} else if runtime.GOOS == "windows" {
		err := exec.Command("net", "session").Run()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: envforge must be run as administrator")
			fmt.Fprintln(os.Stderr, "  right-click -> Run as administrator")
			os.Exit(1)
		}
	}
}

func resolveManifestDir(override string) string {
	if override != "" {
		return override
	}
	exePath, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exePath), "manifests")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	// also try current working directory
	if info, err := os.Stat("./manifests"); err == nil && info.IsDir() {
		return "./manifests"
	}
	return "" // embedded only
}

func defaultDiskPath() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

func printList(categories []manifest.Category) {
	for _, cat := range categories {
		fmt.Printf("\n== %s ==  (%d packages)\n", cat.Name, cat.TotalPackages())
		for _, sub := range cat.Subcategories {
			fmt.Printf("  -- %s --\n", sub.Name)
			for _, pkg := range sub.Packages {
				def := " "
				if pkg.Default {
					def = "*"
				}
				fmt.Printf("    [%s] %-22s  %s\n", def, pkg.Name, pkg.Description)
			}
		}
	}
	fmt.Println("\n  [*] = selected by default")
}

func printManifestInfo(res *manifest.LoadResult, filtered []manifest.Category) {
	fmt.Printf("\nManifest sources:\n")
	fmt.Printf("  embedded:    %d categories\n", res.EmbedCount)
	if res.OverridePath != "" {
		fmt.Printf("  override dir: %s\n", res.OverridePath)
		fmt.Printf("  override:    %d categories\n", res.OverrideCount)
	} else {
		fmt.Printf("  override dir: (none)\n")
	}

	fmt.Printf("\nEffective categories (after platform filter):\n")
	for _, src := range res.Sources {
		// find category info from filtered (it may have been filtered out)
		var pkgs int
		for _, c := range filtered {
			if c.ID == src.CategoryID {
				pkgs = c.TotalPackages()
			}
		}
		fmt.Printf("  %-22s  %s  (%d pkgs after filter)\n", src.CategoryID, src.Origin, pkgs)
	}

	total := 0
	for _, c := range filtered {
		total += c.TotalPackages()
	}
	fmt.Printf("\nTotal installable packages: %d\n", total)
}

func printProfiles() {
	dir := profile.DefaultDir()
	names, err := profile.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("profile directory: %s\n\n", dir)
	if len(names) == 0 {
		fmt.Println("(no profiles found)")
		fmt.Println("\ntip: run `envforge --save mydev` after configuring in TUI")
		return
	}
	sort.Strings(names)
	for _, n := range names {
		p, err := profile.Load(filepath.Join(dir, n+".json"))
		if err != nil {
			fmt.Printf("  %-20s  <error: %v>\n", n, err)
			continue
		}
		fmt.Printf("  %-20s  %d packages   updated: %s\n",
			n, len(p.Selected), p.UpdatedAt.Format("2006-01-02 15:04"))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
