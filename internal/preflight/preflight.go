package preflight

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/envforge/envforge/internal/executor"
	"github.com/envforge/envforge/internal/manifest"
)

const UnknownSizeKB int64 = -1

type InternetResult struct {
	OK      bool
	Checked []string
	Error   string
}

type DiskResult struct {
	Path       string
	FreeBytes  uint64
	TotalBytes uint64
	Error      string
}

type PackageState struct {
	Key       string
	Installed bool
	SizeKB    int64 // estimated installed footprint; UnknownSizeKB means unknown
	SizeKnown bool
	Method    string
	Error     string
}

type Result struct {
	Internet InternetResult
	Disk     DiskResult
	Packages map[string]PackageState
}

func Run(ctx context.Context, categories []manifest.Category, osName string, diskPath string) Result {
	if diskPath == "" {
		diskPath = "/"
		if runtime.GOOS == "windows" {
			diskPath = `C:\`
		}
	}

	internet := CheckInternet(ctx)
	disk := CheckDisk(ctx, diskPath)
	packages := ScanPackages(ctx, categories, osName, 8)

	return Result{
		Internet: internet,
		Disk:     disk,
		Packages: packages,
	}
}

func CheckInternet(ctx context.Context) InternetResult {
	endpoints := []string{
		"https://github.com/",
		"https://deb.debian.org/",
		"https://www.google.com/generate_204",
	}

	client := &http.Client{Timeout: 4 * time.Second}
	var errs []string

	for _, endpoint := range endpoints {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			cancel()
			errs = append(errs, endpoint+": "+err.Error())
			continue
		}
		req.Header.Set("User-Agent", "envforge-preflight")

		resp, err := client.Do(req)
		cancel()
		if err != nil {
			errs = append(errs, endpoint+": "+err.Error())
			continue
		}
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return InternetResult{OK: true, Checked: endpoints}
		}

		errs = append(errs, fmt.Sprintf("%s: HTTP %d", endpoint, resp.StatusCode))
	}

	return InternetResult{
		OK:      false,
		Checked: endpoints,
		Error:   strings.Join(errs, "; "),
	}
}

func CheckDisk(ctx context.Context, path string) DiskResult {
	if runtime.GOOS == "windows" {
		return checkDiskWindows(ctx, path)
	}
	return checkDiskUnix(ctx, path)
}

func checkDiskUnix(ctx context.Context, path string) DiskResult {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(cmdCtx, "df", "-Pk", path).Output()
	if err != nil {
		return DiskResult{Path: path, Error: err.Error()}
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return DiskResult{Path: path, Error: "unexpected df output"}
	}

	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 6 {
		return DiskResult{Path: path, Error: "unexpected df fields"}
	}

	totalKB, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return DiskResult{Path: path, Error: "parse total: " + err.Error()}
	}
	freeKB, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return DiskResult{Path: path, Error: "parse free: " + err.Error()}
	}

	return DiskResult{
		Path:       path,
		TotalBytes: totalKB * 1024,
		FreeBytes:  freeKB * 1024,
	}
}

func checkDiskWindows(ctx context.Context, path string) DiskResult {
	drive := filepath.VolumeName(path)
	if drive == "" {
		drive = "C:"
	}
	driveName := strings.TrimSuffix(drive, ":")

	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ps := fmt.Sprintf(`$d = Get-PSDrive -Name %s; Write-Output "$($d.Free) $($d.Used)"`, driveName)
	out, err := exec.CommandContext(cmdCtx, "powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return DiskResult{Path: path, Error: err.Error()}
	}

	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		return DiskResult{Path: path, Error: "unexpected powershell output"}
	}

	free, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return DiskResult{Path: path, Error: "parse free: " + err.Error()}
	}
	used, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return DiskResult{Path: path, Error: "parse used: " + err.Error()}
	}

	return DiskResult{Path: path, FreeBytes: free, TotalBytes: free + used}
}

func ScanPackages(ctx context.Context, categories []manifest.Category, osName string, workers int) map[string]PackageState {
	if workers <= 0 {
		workers = 4
	}

	type job struct {
		key string
		pkg manifest.Package
		plt manifest.Platform
	}

	jobs := make(chan job)
	results := make(chan PackageState)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results <- scanOne(ctx, j.key, j.pkg, j.plt)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, cat := range categories {
			for _, sub := range cat.Subcategories {
				for _, pkg := range sub.Packages {
					plt, ok := pkg.Platforms[osName]
					if !ok {
						continue
					}
					key := cat.ID + "." + sub.ID + "." + pkg.ID
					jobs <- job{key: key, pkg: pkg, plt: plt}
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make(map[string]PackageState)
	for st := range results {
		out[st.Key] = st
	}
	return out
}

func scanOne(ctx context.Context, key string, pkg manifest.Package, plat manifest.Platform) PackageState {
	st := PackageState{
		Key:    key,
		SizeKB: UnknownSizeKB,
		Method: plat.Method,
	}

	if plat.Check != "" {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st.Installed = executor.CheckShellSucceeds(checkCtx, plat.Check)
		cancel()
	}

	sizeKB, known := EstimatePackageSizeKB(ctx, pkg, plat)
	if known {
		st.SizeKB = sizeKB
		st.SizeKnown = true
	}

	return st
}

func estimateAptSizeKB(ctx context.Context, packages []string) (int64, bool) {
	if len(packages) == 0 || runtime.GOOS == "windows" {
		return UnknownSizeKB, false
	}

	var total int64
	known := false

	seen := make(map[string]bool)
	for _, name := range packages {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		cmdCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		out, err := exec.CommandContext(cmdCtx, "apt-cache", "show", "--no-all-versions", name).Output()
		cancel()
		if err != nil {
			continue
		}

		size, ok := parseAptInstalledSizeKB(string(out))
		if ok {
			total += size
			known = true
		}
	}

	if !known {
		return UnknownSizeKB, false
	}
	return total, true
}

func parseAptInstalledSizeKB(s string) (int64, bool) {
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "Installed-Size:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "Installed-Size:"))
		n, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return n, true
		}
	}
	return UnknownSizeKB, false
}

func estimateCustomGitHubSizeKB(ctx context.Context, plat manifest.Platform) (int64, bool) {
	for _, cmd := range plat.Commands {
		if size, ok := estimateGitHubRepoSizeKB(ctx, firstGitHubRepoURL(cmd)); ok {
			return size, true
		}
	}
	if plat.URL != "" {
		return estimateGitHubRepoSizeKB(ctx, plat.URL)
	}
	return UnknownSizeKB, false
}

var githubURLRe = regexp.MustCompile(`https?://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`) // repo suffix is cleaned below

func firstGitHubRepoURL(s string) string {
	m := githubURLRe.FindStringSubmatch(s)
	if len(m) != 3 {
		return ""
	}
	repo := strings.TrimSuffix(m[2], ".git")
	return "https://github.com/" + m[1] + "/" + repo
}

func estimateGitHubRepoSizeKB(ctx context.Context, repoURL string) (int64, bool) {
	if repoURL == "" {
		return UnknownSizeKB, false
	}

	u, err := url.Parse(repoURL)
	if err != nil || !strings.EqualFold(u.Host, "github.com") {
		return UnknownSizeKB, false
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return UnknownSizeKB, false
	}

	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	apiURL := "https://api.github.com/repos/" + owner + "/" + repo

	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return UnknownSizeKB, false
	}
	req.Header.Set("User-Agent", "envforge-preflight")

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return UnknownSizeKB, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UnknownSizeKB, false
	}

	var body struct {
		Size int64 `json:"size"` // GitHub returns KB
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return UnknownSizeKB, false
	}
	if body.Size <= 0 {
		return UnknownSizeKB, false
	}

	return body.Size, true
}

func FormatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func FormatSizeKB(kb int64) string {
	if kb < 0 {
		return "?"
	}
	return FormatBytes(uint64(kb) * 1024)
}

func EstimatePackageSizeKB(ctx context.Context, pkg manifest.Package, plat manifest.Platform) (int64, bool) {
	if plat.SizeKB > 0 {
		return plat.SizeKB, true
	}

	switch plat.Method {
	case "apt":
		return estimateAptSizeKB(ctx, plat.Packages)
	case "git":
		return estimateGitHubRepoSizeKB(ctx, plat.URL)
	case "cargo":
		return 45_000, true // ~45MB
	case "warp":
		return 180_000, true // ~180MB
	case "mise":
		return 25_000, true // ~25MB
	case "npm":
		return 80_000, true
	case "pip", "pipx":
		return 30_000, true
	case "custom", "script":
		if size, ok := estimateGitHubRepoSizeKB(ctx, plat.URL); ok {
			return size, true
		}
		if strings.Contains(plat.URL, "warp.dev") {
			return 180_000, true
		}
		if strings.Contains(plat.URL, "mise.run") {
			return 25_000, true
		}
	}

	return UnknownSizeKB, false
}
