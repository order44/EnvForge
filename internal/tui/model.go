package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/envforge/envforge/internal/installer"
	"github.com/envforge/envforge/internal/logger"
	"github.com/envforge/envforge/internal/manifest"
	"github.com/envforge/envforge/internal/platform"
	"github.com/envforge/envforge/internal/preflight"
)

type Screen int

const (
	ScreenCategories Screen = iota
	ScreenPackages
	ScreenInstalling
	ScreenSummary
)

// Model is the root TUI state.
type Model struct {
	categories []manifest.Category
	osInfo     platform.OSInfo
	registry   *installer.Registry

	// preflight data
	pkgState map[string]preflight.PackageState
	internet preflight.InternetResult
	disk     preflight.DiskResult

	screen     Screen
	selected   map[string]bool
	expanded   map[string]bool
	catCursor  int
	viewingCat int
	pkgCursor  int
	pkgItems   []pkgListItem

	installQueue   []installItem
	installCurrent int
	installDone    bool
	installStart   time.Time
	currentStart   time.Time
	results        []installer.InstallResult

	// cancellation
	installCtx    context.Context
	installCancel context.CancelFunc

	// streaming output of current package
	streamLines []string

	// summary scroll
	summaryCursor int

	// dimensions
	width  int
	height int

	// status banner shown on the bottom of the categories screen
	banner    string
	bannerExp time.Time
}

type pkgListItem struct {
	isSub   bool
	subName string
	pkg     manifest.Package
	key     string
}

type installItem struct {
	pkg   manifest.Package
	catID string
	subID string
}

// Messages exchanged with bubbletea
type installResultMsg struct{ Result installer.InstallResult }
type streamLineMsg struct{ Line string }
type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// NewModel creates the initial TUI model.
func NewModel(
	categories []manifest.Category,
	osInfo platform.OSInfo,
	reg *installer.Registry,
	pkgState map[string]preflight.PackageState,
	internet preflight.InternetResult,
	disk preflight.DiskResult,
) *Model {
	if pkgState == nil {
		pkgState = make(map[string]preflight.PackageState)
	}

	m := &Model{
		categories: categories,
		osInfo:     osInfo,
		registry:   reg,
		pkgState:   pkgState,
		internet:   internet,
		disk:       disk,
		selected:   make(map[string]bool),
		expanded:   make(map[string]bool),
		screen:     ScreenCategories,
		width:      80,
		height:     24,
	}

	// Select defaults, but do not select already-installed packages.
	for _, cat := range categories {
		for _, k := range cat.DefaultPackageKeys() {
			if !m.isInstalled(k) {
				m.selected[k] = true
			}
		}
	}

	return m
}

// ApplyProfile loads a selection map (e.g. from a profile file) into the model.
func (m *Model) ApplyProfile(selection map[string]bool) {
	m.selected = make(map[string]bool, len(selection))
	for k, v := range selection {
		if v && !m.isInstalled(k) {
			m.selected[k] = true
		}
	}
}

func (m *Model) Selection() map[string]bool {
	out := make(map[string]bool, len(m.selected))
	for k, v := range m.selected {
		if v && !m.isInstalled(k) {
			out[k] = true
		}
	}
	return out
}

// Init satisfies tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Update handles all incoming messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch m.screen {
		case ScreenCategories:
			return m.updateCategories(msg)
		case ScreenPackages:
			return m.updatePackages(msg)
		case ScreenInstalling:
			return m.updateInstalling(msg)
		case ScreenSummary:
			return m.updateSummary(msg)
		}

	case installResultMsg:
		return m.handleInstallResult(msg)

	case streamLineMsg:
		m.streamLines = append(m.streamLines, msg.Line)
		if len(m.streamLines) > 5 {
			m.streamLines = m.streamLines[len(m.streamLines)-5:]
		}
		return m, nil

	case tickMsg:
		if m.screen == ScreenInstalling && !m.installDone {
			return m, tickCmd()
		}
		return m, nil
	}
	return m, nil
}

// View renders the current screen.
func (m *Model) View() string {
	switch m.screen {
	case ScreenCategories:
		return m.viewCategories()
	case ScreenPackages:
		return m.viewPackages()
	case ScreenInstalling:
		return m.viewInstalling()
	case ScreenSummary:
		return m.viewSummary()
	}
	return ""
}

// ---------------------------------------------------------------------------
// categories screen
// ---------------------------------------------------------------------------

func (m *Model) updateCategories(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.catCursor > 0 {
			m.catCursor--
		}
	case "down", "j":
		if m.catCursor < len(m.categories)-1 {
			m.catCursor++
		}
	case " ":
		cat := m.categories[m.catCursor]
		keys := cat.AllPackageKeys()
		allOn := true
		for _, k := range keys {
			if m.isInstalled(k) {
				continue
			}
			if !m.selected[k] {
				allOn = false
				break
			}
		}
		for _, k := range keys {
			if m.isInstalled(k) {
				delete(m.selected, k)
				continue
			}
			m.selected[k] = !allOn
		}
	case "enter":
		m.viewingCat = m.catCursor
		m.pkgCursor = 0
		m.buildPkgList()
		m.movePkgCursor(1)
		m.screen = ScreenPackages
	case "a":
		for _, cat := range m.categories {
			for _, k := range cat.AllPackageKeys() {
				if !m.isInstalled(k) {
					m.selected[k] = true
				}
			}
		}
	case "n":
		m.selected = make(map[string]bool)
	case "d":
		// reset to defaults, excluding already-installed packages
		m.selected = make(map[string]bool)
		for _, cat := range m.categories {
			for _, k := range cat.DefaultPackageKeys() {
				if !m.isInstalled(k) {
					m.selected[k] = true
				}
			}
		}
		m.setBanner("selection reset to defaults; installed packages are skipped")
	case "i":
		if m.totalSelected() == 0 {
			m.setBanner("nothing selected; installed packages are skipped")
			return m, nil
		}
		m.startInstall()
		return m, tea.Batch(m.installNext(), tickCmd())
	}
	return m, nil
}

func (m *Model) viewCategories() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf(" EnvForge  %s ", m.osInfo.String())))
	if m.registry != nil && m.registry.DryRun() {
		b.WriteString("  " + statusRunStyle.Render("[DRY-RUN]"))
	}
	b.WriteString("\n")
	b.WriteString(m.preflightLine())
	b.WriteString("\n\n")
	b.WriteString(subtitleStyle.Render("Select categories to install:"))
	b.WriteString("\n\n")

	installedTotal := m.totalInstalled()
	for i, cat := range m.categories {
		cursor := "  "
		if i == m.catCursor {
			cursor = cursorStyle.Render("> ")
		}
		sel := cat.SelectedCount(m.selected)
		installed := m.categoryInstalledCount(cat)
		total := cat.TotalPackages()
		selectable := total - installed
		if selectable < 0 {
			selectable = 0
		}

		cb := unselectedStyle.Render("[ ]")
		if selectable > 0 && sel == selectable {
			cb = selectedStyle.Render("[x]")
		} else if sel > 0 {
			cb = statusRunStyle.Render("[~]")
		} else if installed == total && total > 0 {
			cb = statusSkipStyle.Render("[-]")
		}

		b.WriteString(fmt.Sprintf("%s%s %s %s\n",
			cursor, cb,
			categoryStyle.Render(cat.Name),
			descStyle.Render(fmt.Sprintf("(%d selected, %d installed, %d total)", sel, installed, total)),
		))
	}

	b.WriteString("\n")
	b.WriteString(counterStyle.Render(fmt.Sprintf(" Selected: %d   Estimated additional size: %s   Installed detected: %d",
		m.totalSelected(), m.selectedSizeLabel(), installedTotal)))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(
		"[↑/↓] navigate  [space] toggle  [enter] expand  [i] install\n" +
			" [a] all not-installed  [n] none  [d] defaults  [q] quit",
	))
	if m.banner != "" && time.Now().Before(m.bannerExp) {
		b.WriteString("\n")
		b.WriteString(statusRunStyle.Render(" ! " + m.banner))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// packages screen
// ---------------------------------------------------------------------------

func (m *Model) buildPkgList() {
	m.pkgItems = nil
	cat := m.categories[m.viewingCat]
	for _, sub := range cat.Subcategories {
		m.pkgItems = append(m.pkgItems, pkgListItem{isSub: true, subName: sub.Name})
		for _, pkg := range sub.Packages {
			m.pkgItems = append(m.pkgItems, pkgListItem{
				pkg: pkg,
				key: cat.ID + "." + sub.ID + "." + pkg.ID,
			})
		}
	}
}

func (m *Model) updatePackages(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.screen = ScreenCategories
	case "up", "k":
		m.movePkgCursor(-1)
	case "down", "j":
		m.movePkgCursor(1)
	case " ":
		if m.pkgCursor < len(m.pkgItems) {
			item := m.pkgItems[m.pkgCursor]
			if !item.isSub {
				if m.isInstalled(item.key) {
					delete(m.selected, item.key)
					m.setBanner(item.pkg.Name + " already installed; skipped")
				} else {
					m.selected[item.key] = !m.selected[item.key]
				}
			}
		}
	case "a":
		for _, k := range m.categories[m.viewingCat].AllPackageKeys() {
			if !m.isInstalled(k) {
				m.selected[k] = true
			}
		}
	case "n":
		for _, k := range m.categories[m.viewingCat].AllPackageKeys() {
			delete(m.selected, k)
		}
	case "i":
		if m.totalSelected() == 0 {
			m.setBanner("nothing selected")
			return m, nil
		}
		m.startInstall()
		return m, tea.Batch(m.installNext(), tickCmd())
	}
	return m, nil
}

func (m *Model) movePkgCursor(delta int) {
	if len(m.pkgItems) == 0 {
		return
	}
	m.pkgCursor += delta
	if m.pkgCursor < 0 {
		m.pkgCursor = 0
	}
	if m.pkgCursor >= len(m.pkgItems) {
		m.pkgCursor = len(m.pkgItems) - 1
	}
	// skip subcategory headers
	for m.pkgCursor >= 0 && m.pkgCursor < len(m.pkgItems) && m.pkgItems[m.pkgCursor].isSub {
		m.pkgCursor += delta
		if m.pkgCursor < 0 {
			m.pkgCursor = 0
			break
		}
		if m.pkgCursor >= len(m.pkgItems) {
			m.pkgCursor = len(m.pkgItems) - 1
			break
		}
	}
}

func (m *Model) viewPackages() string {
	var b strings.Builder
	cat := m.categories[m.viewingCat]
	b.WriteString(headerStyle.Render(fmt.Sprintf(" %s ", cat.Name)))
	b.WriteString("\n")
	b.WriteString(m.preflightLine())
	b.WriteString("\n\n")

	vis := m.height - 9
	if vis < 5 {
		vis = 5
	}
	start := 0
	if m.pkgCursor >= vis {
		start = m.pkgCursor - vis + 1
	}
	end := start + vis
	if end > len(m.pkgItems) {
		end = len(m.pkgItems)
	}

	for i := start; i < end; i++ {
		item := m.pkgItems[i]
		if item.isSub {
			b.WriteString(titleStyle.Render(fmt.Sprintf("  -- %s --", item.subName)))
			b.WriteString("\n")
			continue
		}
		cursor := "  "
		if i == m.pkgCursor {
			cursor = cursorStyle.Render("> ")
		}

		cb := unselectedStyle.Render("[ ]")
		if m.isInstalled(item.key) {
			cb = statusSkipStyle.Render("[-]")
		} else if m.selected[item.key] {
			cb = selectedStyle.Render("[x]")
		}

		status := m.packageStatusLabel(item.key)
		size := m.packageSizeLabel(item.key)
		b.WriteString(fmt.Sprintf("  %s%s %-22s %-12s %-10s %s\n",
			cursor, cb,
			packageStyle.Render(item.pkg.Name),
			status,
			descStyle.Render(size),
			descStyle.Render("- "+item.pkg.Description),
		))
	}

	b.WriteString("\n")
	b.WriteString(counterStyle.Render(fmt.Sprintf(" %d selected in this category   total selected size: %s",
		cat.SelectedCount(m.selected), m.selectedSizeLabel())))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(
		"[↑/↓] navigate  [space] toggle  [a] all not-installed  [n] none  [esc] back  [i] install",
	))
	if m.banner != "" && time.Now().Before(m.bannerExp) {
		b.WriteString("\n")
		b.WriteString(statusRunStyle.Render(" ! " + m.banner))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// installing screen
// ---------------------------------------------------------------------------

func (m *Model) startInstall() {
	m.screen = ScreenInstalling
	m.installQueue = nil
	m.installCurrent = 0
	m.installDone = false
	m.results = nil
	m.streamLines = nil
	m.summaryCursor = 0
	m.installStart = time.Now()
	m.currentStart = time.Now()
	m.installCtx, m.installCancel = context.WithCancel(context.Background())

	for _, cat := range m.categories {
		for _, sub := range cat.Subcategories {
			for _, pkg := range sub.Packages {
				key := cat.ID + "." + sub.ID + "." + pkg.ID
				if m.selected[key] && !m.isInstalled(key) {
					m.installQueue = append(m.installQueue, installItem{
						pkg: pkg, catID: cat.ID, subID: sub.ID,
					})
				}
			}
		}
	}
}

func (m *Model) installNext() tea.Cmd {
	if m.installCurrent >= len(m.installQueue) {
		return nil
	}
	item := m.installQueue[m.installCurrent]
	reg := m.registry
	ctx := m.installCtx
	m.currentStart = time.Now()
	m.streamLines = nil

	return func() tea.Msg {
		return installResultMsg{Result: reg.InstallPackage(ctx, item.pkg)}
	}
}

func (m *Model) handleInstallResult(msg installResultMsg) (tea.Model, tea.Cmd) {
	m.results = append(m.results, msg.Result)
	m.installCurrent++
	m.streamLines = nil

	if m.installCurrent >= len(m.installQueue) {
		m.installDone = true
		if m.installCancel != nil {
			m.installCancel()
		}
		m.screen = ScreenSummary
		return m, nil
	}
	if m.installCtx.Err() != nil {
		// cancelled mid-flight; finish whatever we have
		m.installDone = true
		m.screen = ScreenSummary
		return m, nil
	}
	return m, m.installNext()
}

func (m *Model) updateInstalling(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.installCancel != nil {
			m.installCancel()
		}
		// don't quit immediately; wait for current task to bail out
		m.setBanner("cancelling, please wait...")
		return m, nil
	case "q":
		if m.installDone {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Model) viewInstalling() string {
	var b strings.Builder
	total := len(m.installQueue)
	cur := m.installCurrent
	if cur > total {
		cur = total
	}
	pct := float64(0)
	if total > 0 {
		pct = float64(cur) / float64(total) * 100
	}

	elapsed := time.Since(m.installStart).Round(time.Second)
	b.WriteString(headerStyle.Render(fmt.Sprintf(" Installing [%d/%d] %.0f%% — elapsed %s ", cur, total, pct, elapsed)))
	b.WriteString("\n")

	// progress bar
	barW := m.width - 4
	if barW < 20 {
		barW = 20
	}
	if barW > 80 {
		barW = 80
	}
	filled := int(float64(barW) * pct / 100)
	if filled > barW {
		filled = barW
	}
	bar := " " +
		progressBarFilled.Render(strings.Repeat("█", filled)) +
		progressBarEmpty.Render(strings.Repeat("░", barW-filled))
	b.WriteString(bar)
	b.WriteString("\n\n")

	// completed results (tail)
	vis := m.height - 14
	if vis < 3 {
		vis = 3
	}
	startIdx := 0
	if len(m.results) > vis {
		startIdx = len(m.results) - vis
	}
	for i := startIdx; i < len(m.results); i++ {
		b.WriteString(formatResult(m.results[i]))
		b.WriteString("\n")
	}

	// current package
	if cur < total {
		item := m.installQueue[cur]
		dur := time.Since(m.currentStart).Round(time.Second)
		b.WriteString(statusRunStyle.Render(fmt.Sprintf(" ~ %-22s", item.pkg.Name)))
		b.WriteString(descStyle.Render(fmt.Sprintf(" [installing... %s]", dur)))
		b.WriteString("\n")

		// streamed lines from current install
		for _, line := range m.streamLines {
			if len(line) > m.width-6 {
				line = line[:m.width-9] + "..."
			}
			b.WriteString(streamLineStyle.Render(line))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	if m.banner != "" && time.Now().Before(m.bannerExp) {
		b.WriteString(statusRunStyle.Render(" ! " + m.banner))
		b.WriteString("\n")
	}
	b.WriteString(helpStyle.Render(" [ctrl+c] cancel"))
	return m.padToHeight(b.String())
}

// ---------------------------------------------------------------------------
// summary screen
// ---------------------------------------------------------------------------

func (m *Model) updateSummary(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "enter":
		return m, tea.Quit
	case "up", "k":
		if m.summaryCursor > 0 {
			m.summaryCursor--
		}
	case "down", "j":
		if m.summaryCursor < len(m.results)-1 {
			m.summaryCursor++
		}
	case "g":
		m.summaryCursor = 0
	case "G":
		m.summaryCursor = len(m.results) - 1
	}
	return m, nil
}

func (m *Model) viewSummary() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(" EnvForge — Installation Complete "))
	b.WriteString("\n\n")

	ok, fail, skip := 0, 0, 0
	for _, r := range m.results {
		switch r.Status {
		case installer.StatusSuccess:
			ok++
		case installer.StatusFailed:
			fail++
		case installer.StatusSkipped:
			skip++
		}
	}

	elapsed := time.Since(m.installStart).Round(time.Second)
	b.WriteString(statusOKStyle.Render(fmt.Sprintf(" ✓ installed: %d", ok)))
	b.WriteString("\n")
	if fail > 0 {
		b.WriteString(statusFailStyle.Render(fmt.Sprintf(" ✗ failed:    %d", fail)))
		b.WriteString("\n")
	}
	if skip > 0 {
		b.WriteString(statusSkipStyle.Render(fmt.Sprintf(" - skipped:   %d", skip)))
		b.WriteString("\n")
	}
	b.WriteString(descStyle.Render(fmt.Sprintf("   total time: %s", elapsed)))
	b.WriteString("\n")
	if path := logger.LogPath(); path != "" {
		b.WriteString(descStyle.Render(fmt.Sprintf("   log: %s", path)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render("All packages (↑/↓ scroll, g/G top/bottom):"))
	b.WriteString("\n")

	vis := m.height - 14
	if vis < 5 {
		vis = 5
	}
	scrollStart := 0
	if m.summaryCursor >= vis {
		scrollStart = m.summaryCursor - vis + 1
	}
	scrollEnd := scrollStart + vis
	if scrollEnd > len(m.results) {
		scrollEnd = len(m.results)
	}

	for i := scrollStart; i < scrollEnd; i++ {
		r := m.results[i]
		cursor := "  "
		if i == m.summaryCursor {
			cursor = cursorStyle.Render("> ")
		}
		b.WriteString(cursor)
		b.WriteString(formatResult(r))

		if i == m.summaryCursor && r.Status == installer.StatusFailed && r.Error != "" {
			lines := strings.Split(strings.TrimSpace(r.Error), "\n")
			maxLines := 4
			if len(lines) < maxLines {
				maxLines = len(lines)
			}
			for _, line := range lines[:maxLines] {
				if len(line) > m.width-8 {
					line = line[:m.width-11] + "..."
				}
				b.WriteString("\n")
				b.WriteString(dimStyle.Render("      " + line))
			}
		}
		b.WriteString("\n")
	}

	if len(m.results) > vis {
		b.WriteString(dimStyle.Render(fmt.Sprintf("\n   showing %d–%d of %d", scrollStart+1, scrollEnd, len(m.results))))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render(" [↑/↓] scroll  [enter/q] quit"))
	return b.String()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (m *Model) totalSelected() int {
	n := 0
	for k, v := range m.selected {
		if v && !m.isInstalled(k) {
			n++
		}
	}
	return n
}

func (m *Model) totalInstalled() int {
	n := 0
	for _, st := range m.pkgState {
		if st.Installed {
			n++
		}
	}
	return n
}

func (m *Model) isInstalled(key string) bool {
	st, ok := m.pkgState[key]
	return ok && st.Installed
}

func (m *Model) packageState(key string) preflight.PackageState {
	if st, ok := m.pkgState[key]; ok {
		return st
	}
	return preflight.PackageState{Key: key, SizeKB: preflight.UnknownSizeKB}
}

func (m *Model) categoryInstalledCount(cat manifest.Category) int {
	n := 0
	for _, key := range cat.AllPackageKeys() {
		if m.isInstalled(key) {
			n++
		}
	}
	return n
}

func (m *Model) selectedSize() (knownKB int64, unknown int) {
	for k, v := range m.selected {
		if !v || m.isInstalled(k) {
			continue
		}
		st := m.packageState(k)
		if st.SizeKnown && st.SizeKB >= 0 {
			knownKB += st.SizeKB
		} else {
			unknown++
		}
	}
	return knownKB, unknown
}

func (m *Model) selectedSizeLabel() string {
	knownKB, unknown := m.selectedSize()
	label := "~" + preflight.FormatSizeKB(knownKB)
	if unknown > 0 {
		label += fmt.Sprintf(" + %d unknown", unknown)
	}
	return label
}

func (m *Model) packageSizeLabel(key string) string {
	st := m.packageState(key)
	if st.SizeKnown {
		return preflight.FormatSizeKB(st.SizeKB)
	}
	return "size ?"
}

func (m *Model) packageStatusLabel(key string) string {
	if m.isInstalled(key) {
		return statusSkipStyle.Render("installed")
	}
	return statusRunStyle.Render("will install")
}

func (m *Model) preflightLine() string {
	internet := statusFailStyle.Render("net: offline")
	if m.internet.OK {
		internet = statusOKStyle.Render("net: ok")
	}

	disk := statusSkipStyle.Render("disk: ?")
	if m.disk.Error == "" && m.disk.FreeBytes > 0 {
		disk = statusOKStyle.Render("disk free: " + preflight.FormatBytes(m.disk.FreeBytes))
	}

	return fmt.Sprintf(" %s  %s  %s", internet, disk, descStyle.Render("selected size: "+m.selectedSizeLabel()))
}

func (m *Model) setBanner(text string) {
	m.banner = text
	m.bannerExp = time.Now().Add(4 * time.Second)
}

func (m *Model) padToHeight(s string) string {
	if m.height <= 0 {
		return s
	}
	lineCount := strings.Count(s, "\n") + 1
	if lineCount >= m.height {
		return s
	}
	return s + strings.Repeat("\n", m.height-lineCount)
}

func formatResult(r installer.InstallResult) string {
	var icon string
	var style lipgloss.Style
	switch r.Status {
	case installer.StatusSuccess:
		icon = "✓"
		style = statusOKStyle
	case installer.StatusFailed:
		icon = "✗"
		style = statusFailStyle
	case installer.StatusSkipped:
		icon = "-"
		style = statusSkipStyle
	default:
		icon = "."
		style = statusSkipStyle
	}
	dur := ""
	if r.Duration > 0 {
		dur = descStyle.Render(fmt.Sprintf("  %s", r.Duration.Round(100*time.Millisecond)))
	}
	return style.Render(fmt.Sprintf(" %s %-24s", icon, r.PackageName)) + dur
}
