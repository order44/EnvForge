package manifest

// Category represents a top-level category of packages
type Category struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	Subcategories []Subcategory `json:"subcategories"`
}

// Subcategory groups related packages
type Subcategory struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Packages []Package `json:"packages"`
}

// Package describes a single installable package
type Package struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	URL         string              `json:"url,omitempty"`
	Default     bool                `json:"default"`
	Tags        []string            `json:"tags,omitempty"`
	Platforms   map[string]Platform `json:"platforms"`
}

// Platform describes how to install a package on a specific OS
type Platform struct {
	Method       string   `json:"method"`
	Packages     []string `json:"packages,omitempty"`
	ID           string   `json:"id,omitempty"`
	URL          string   `json:"url,omitempty"`
	Owner        string   `json:"owner,omitempty"`
	Repo         string   `json:"repo,omitempty"`
	AssetPattern string   `json:"asset_pattern,omitempty"`
	InstallDir   string   `json:"install_dir,omitempty"`
	Commands     []string `json:"commands,omitempty"`
	PostInstall  []string `json:"post_install,omitempty"`
	Note         string   `json:"note,omitempty"`

	// SizeKB optionally overrides automatic size estimation.
	// Use this for custom/script/npm/pip tools where size cannot be estimated reliably
	// without downloading or installing the artifact.
	SizeKB int64 `json:"size_kb,omitempty"`

	// Check is a shell command that determines if package is already installed.
	// If the command exits with code 0, package is considered installed and is skipped.
	// Examples: "command -v rg", "dpkg -s curl >/dev/null 2>&1", "test -d /opt/ghidra*"
	Check string `json:"check,omitempty"`

	// Verify optionally describes how to verify downloaded artifacts.
	// Only used by methods that download files (archive, binary, script).
	Verify *Verification `json:"verify,omitempty"`

	// RequiresAptUpdate forces apt-get update before installation.
	// Useful for packages installed from repos added by previous steps.
	RequiresAptUpdate bool `json:"requires_apt_update,omitempty"`
}

// Verification describes how to check integrity of a downloaded artifact.
type Verification struct {
	Type  string `json:"type"`  // "sha256" | "sha512" | "gpg"
	Value string `json:"value"` // expected hash or signature url
}

func (c *Category) TotalPackages() int {
	total := 0
	for _, sub := range c.Subcategories {
		total += len(sub.Packages)
	}
	return total
}

func (c *Category) SelectedCount(selected map[string]bool) int {
	count := 0
	for _, sub := range c.Subcategories {
		for _, pkg := range sub.Packages {
			key := c.ID + "." + sub.ID + "." + pkg.ID
			if selected[key] {
				count++
			}
		}
	}
	return count
}

func (c *Category) InstalledCount(installed map[string]bool) int {
	count := 0
	for _, sub := range c.Subcategories {
		for _, pkg := range sub.Packages {
			key := c.ID + "." + sub.ID + "." + pkg.ID
			if installed[key] {
				count++
			}
		}
	}
	return count
}

func (c *Category) AllPackageKeys() []string {
	var keys []string
	for _, sub := range c.Subcategories {
		for _, pkg := range sub.Packages {
			keys = append(keys, c.ID+"."+sub.ID+"."+pkg.ID)
		}
	}
	return keys
}

func (c *Category) DefaultPackageKeys() []string {
	var keys []string
	for _, sub := range c.Subcategories {
		for _, pkg := range sub.Packages {
			if pkg.Default {
				keys = append(keys, c.ID+"."+sub.ID+"."+pkg.ID)
			}
		}
	}
	return keys
}
