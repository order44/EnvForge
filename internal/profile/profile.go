package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Profile is a saved set of packages a user wants to install.
type Profile struct {
	Version   string    `json:"version"`
	Name      string    `json:"name,omitempty"`
	Platform  string    `json:"platform,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Selected  []string  `json:"selected"`
}

const currentVersion = "1.0"

// New creates an empty profile.
func New(name, platform string) *Profile {
	now := time.Now()
	return &Profile{
		Version:   currentVersion,
		Name:      name,
		Platform:  platform,
		CreatedAt: now,
		UpdatedAt: now,
		Selected:  nil,
	}
}

// FromMap creates a profile from a selection map (key -> selected).
func FromMap(name, platform string, selected map[string]bool) *Profile {
	p := New(name, platform)
	for k, v := range selected {
		if v {
			p.Selected = append(p.Selected, k)
		}
	}
	sort.Strings(p.Selected)
	return p
}

// ToMap converts the profile's selection into a map for use in the TUI/installer.
func (p *Profile) ToMap() map[string]bool {
	m := make(map[string]bool, len(p.Selected))
	for _, k := range p.Selected {
		m[k] = true
	}
	return m
}

// Save writes the profile to the given path in JSON form.
func (p *Profile) Save(path string) error {
	p.UpdatedAt = time.Now()
	if p.Version == "" {
		p.Version = currentVersion
	}
	sort.Strings(p.Selected)

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profile: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create profile dir: %w", err)
		}
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write profile %q: %w", path, err)
	}
	return nil
}

// Load reads a profile from disk.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profile %q: %w", path, err)
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}
	if p.Version == "" {
		p.Version = currentVersion
	}
	return &p, nil
}

// DefaultDir returns the default directory for storing profiles.
// Linux: $XDG_CONFIG_HOME/envforge/profiles or ~/.config/envforge/profiles
// Windows: %APPDATA%\envforge\profiles
// Respects SUDO_USER on Linux so profiles live in the real user's home.
func DefaultDir() string {
	if dir := os.Getenv("ENVFORGE_PROFILE_DIR"); dir != "" {
		return dir
	}
	home := realUserHome()
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "envforge", "profiles")
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "envforge", "profiles")
	}
	if home != "" {
		return filepath.Join(home, ".config", "envforge", "profiles")
	}
	return filepath.Join(os.TempDir(), "envforge", "profiles")
}

// ResolveProfilePath turns either a bare name ("dev") or a path ("./dev.json")
// into a concrete file path. If the input has no extension and no directory
// component, it's looked up under DefaultDir() with .json appended.
func ResolveProfilePath(input string) string {
	if input == "" {
		return ""
	}
	if filepath.IsAbs(input) {
		return input
	}
	if filepath.Dir(input) != "." {
		return input
	}
	if filepath.Ext(input) == "" {
		return filepath.Join(DefaultDir(), input+".json")
	}
	return filepath.Join(DefaultDir(), input)
}

// List returns the names (without .json) of all profiles in DefaultDir.
func List() ([]string, error) {
	dir := DefaultDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name()[:len(e.Name())-5])
	}
	sort.Strings(names)
	return names, nil
}

func realUserHome() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		// avoid importing os/user to keep this package light; rely on env
		if h := os.Getenv("HOME"); sudoUser == os.Getenv("USER") && h != "" {
			return h
		}
		// fall back to /home/<user> as a best guess
		return filepath.Join("/home", sudoUser)
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
