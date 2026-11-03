package web

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	pw "github.com/mxschmitt/playwright-go"

	"openmini/internal/config"
)

// cacheDir is where Playwright keeps its driver and browsers on this OS.
func cacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local")
	case "darwin":
		return filepath.Join(home, "Library", "Caches")
	}
	return filepath.Join(home, ".cache")
}

// BrowserInstalled reports whether the Playwright driver this build needs and
// a Chromium are already on disk. It downloads nothing.
func BrowserInstalled() bool {
	d, err := pw.NewDriver(&pw.RunOptions{Browsers: []string{"chromium"}})
	if err != nil || cacheDir() == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(cacheDir(), "ms-playwright-go", d.Version, "package", "cli.js")); err != nil {
		return false
	}
	browsers := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if browsers == "" {
		browsers = filepath.Join(cacheDir(), "ms-playwright")
	}
	found, _ := filepath.Glob(filepath.Join(browsers, "chromium-*"))
	return len(found) > 0
}

// InstallBrowser downloads the Playwright driver and Chromium (about 150 MB
// the first time).
func InstallBrowser() error {
	return pw.Install(&pw.RunOptions{Browsers: []string{"chromium"}})
}

// CheckSignedIn opens the profile headless, looks for a signed-in account and
// closes again. The profile must not be in use by a running openmini.
func CheckSignedIn(cfg config.Web) (bool, error) {
	cfg.Headless = true
	w := New(cfg, 0, func(string, ...any) {})
	if err := w.Start(); err != nil {
		return false, err
	}
	defer w.Close()
	if !w.SignedIn() {
		time.Sleep(3 * time.Second) // the account button can render late on a slow start
	}
	return w.SignedIn(), nil
}
