package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type OSType string

const (
	Linux   OSType = "linux"
	Windows OSType = "windows"
	Unknown OSType = "unknown"
)

type OSInfo struct {
	OS       OSType
	Distro   string
	Version  string
	Arch     string
	IsWSL    bool
	IsRoot   bool
	Codename string
}

func Detect() OSInfo {
	info := OSInfo{Arch: runtime.GOARCH}
	switch runtime.GOOS {
	case "linux":
		info.OS = Linux
		info.detectLinux()
	case "windows":
		info.OS = Windows
		info.detectWindows()
	default:
		info.OS = Unknown
	}
	return info
}

func (o *OSInfo) detectLinux() {
	o.IsRoot = os.Geteuid() == 0

	if data, err := os.ReadFile("/proc/version"); err == nil {
		lower := strings.ToLower(string(data))
		o.IsWSL = strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
	}

	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			val := strings.Trim(parts[1], "\"")
			switch parts[0] {
			case "ID":
				o.Distro = val
			case "VERSION_ID":
				o.Version = val
			case "VERSION_CODENAME":
				o.Codename = val
			}
		}
	}
}

func (o *OSInfo) detectWindows() {
	o.IsRoot = isWindowsAdmin()
	o.Distro = "windows"
	if out, err := exec.Command("cmd", "/c", "ver").Output(); err == nil {
		o.Version = strings.TrimSpace(string(out))
	}
}

func isWindowsAdmin() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	_, err := exec.Command("net", "session").Output()
	return err == nil
}

func (o OSInfo) String() string {
	switch o.OS {
	case Linux:
		d := o.Distro
		if d == "" {
			d = "linux"
		}
		v := ""
		if o.Version != "" {
			v = " " + o.Version
		}
		wsl := ""
		if o.IsWSL {
			wsl = " (WSL)"
		}
		return fmt.Sprintf("%s%s %s%s", d, v, o.Arch, wsl)
	case Windows:
		return fmt.Sprintf("windows %s", o.Arch)
	default:
		return "unknown"
	}
}

func (o OSInfo) IsSupported() bool {
	if o.OS == Linux {
		switch o.Distro {
		case "debian", "ubuntu", "kali", "linuxmint", "pop", "raspbian":
			return true
		}
		return false
	}
	return o.OS == Windows
}

func (o OSInfo) PackageManager() string {
	switch o.OS {
	case Linux:
		return "apt"
	case Windows:
		return "winget"
	default:
		return ""
	}
}
