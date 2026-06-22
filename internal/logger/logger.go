package logger

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

var (
	Log     zerolog.Logger
	logFile *os.File
	logPath string

	// TUILines stores recent log lines for display in TUI
	TUILines    []string
	tuiLinesMu  sync.Mutex
	maxTUILines = 200
)

// Init initializes the logger. If logDir is empty, picks an XDG-compliant directory.
// When running under sudo, files are chowned back to the real user so they're readable
// without sudo afterwards.
func Init(logDir string) error {
	if logDir == "" {
		logDir = DefaultLogDir()
	}

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("create log dir %q: %w", logDir, err)
	}

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	logPath = filepath.Join(logDir, fmt.Sprintf("envforge-%s.log", timestamp))

	var err error
	logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file %q: %w", logPath, err)
	}

	// chown to real user when running under sudo
	chownToRealUser(logDir)
	chownToRealUser(logPath)

	multi := io.MultiWriter(logFile, &tuiWriter{})
	Log = zerolog.New(multi).With().Timestamp().Logger()
	zerolog.TimeFieldFormat = time.RFC3339

	Log.Info().Str("log_file", logPath).Msg("logger initialized")
	return nil
}

// LogPath returns the path of the current log file.
func LogPath() string {
	return logPath
}

// Close closes the log file.
func Close() {
	if logFile != nil {
		_ = logFile.Close()
	}
}

// DefaultLogDir returns the platform-appropriate log directory.
// Linux: $XDG_STATE_HOME/envforge/logs or ~/.local/state/envforge/logs
// Windows: %LOCALAPPDATA%\envforge\logs
// When under sudo, uses the real user's home directory, not /root.
func DefaultLogDir() string {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "envforge", "logs")
		}
		return filepath.Join(os.TempDir(), "envforge", "logs")
	}

	// Linux / macOS
	home := realUserHome()

	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "envforge", "logs")
	}
	if home != "" {
		return filepath.Join(home, ".local", "state", "envforge", "logs")
	}
	return filepath.Join(os.TempDir(), "envforge", "logs")
}

// realUserHome returns the home directory of the original user when running under sudo.
func realUserHome() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			return u.HomeDir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// chownToRealUser changes file ownership back to the original user when running under sudo.
func chownToRealUser(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	_ = os.Chown(path, uid, gid)
}

// GetTUILines returns recent log lines for TUI display
func GetTUILines(n int) []string {
	tuiLinesMu.Lock()
	defer tuiLinesMu.Unlock()
	if n <= 0 || n > len(TUILines) {
		n = len(TUILines)
	}
	start := len(TUILines) - n
	if start < 0 {
		start = 0
	}
	result := make([]string, n)
	copy(result, TUILines[start:])
	return result
}

// tuiWriter captures log output for TUI display
type tuiWriter struct{}

func (w *tuiWriter) Write(p []byte) (n int, err error) {
	line := string(p)
	tuiLinesMu.Lock()
	defer tuiLinesMu.Unlock()
	TUILines = append(TUILines, line)
	if len(TUILines) > maxTUILines {
		TUILines = TUILines[len(TUILines)-maxTUILines:]
	}
	return len(p), nil
}
