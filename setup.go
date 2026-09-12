package main

import (
	"openmini/internal/proc"

	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"openmini/internal/backend"
	"openmini/internal/backend/web"
	"openmini/internal/config"
)

// startHint is how this platform starts the server.
func startHint() string {
	if runtime.GOOS == "windows" {
		return "openmini.exe (double-click, or `openmini serve`)"
	}
	return "./run.sh (or `openmini serve`)"
}

// ask prints a yes/no question and reads the answer; empty input means def.
func ask(in *bufio.Reader, q string, def bool) bool {
	if def {
		fmt.Printf("%s [Y/n] ", q)
	} else {
		fmt.Printf("%s [y/N] ", q)
	}
	line, err := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		if err != nil { // no terminal or input ran out: never assume yes
			fmt.Println("(no answer, taking no)")
			return false
		}
		return def
	}
	return line == "y" || line == "yes"
}

// runInteractive runs a program with this terminal attached.
func runInteractive(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// agyState reports whether agy is installed and signed in.
func agyState(bin string) (installed, signedIn bool) {
	if bin == "" {
		bin = "agy"
	}
	bin = backend.FindAgy(bin)
	if _, err := exec.LookPath(bin); err != nil {
		return false, false
	}
	cmd := exec.Command(bin, "models")
	backend.PrepareAgy(cmd)
	cmd.Dir = os.TempDir()
	out, err := cmd.Output()
	return true, err == nil && strings.Contains(string(out), "\t")
}

// installAgy runs Google's installer for this platform.
func installAgy() error {
	if runtime.GOOS == "windows" {
		return runInteractive("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
			"irm https://antigravity.google/cli/install.ps1 | iex")
	}
	return runInteractive("bash", "-c", "curl -fsSL https://antigravity.google/cli/install.sh | bash")
}

// setEnabled flips the "enabled" line of one config section in the file text.
func setEnabled(text, section string, on bool) string {
	re := regexp.MustCompile(`(?ms)^\[` + regexp.QuoteMeta(section) + `\]\n.*?^enabled\s*=\s*(true|false)`)
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return text
	}
	return text[:loc[2]] + fmt.Sprint(on) + text[loc[3]:]
}

// setValue rewrites one `key = "..."` line inside a config section.
func setValue(text, section, key, value string) string {
	re := regexp.MustCompile(`(?ms)^\[` + regexp.QuoteMeta(section) + `\]\n.*?^` + regexp.QuoteMeta(key) + `\s*=\s*"([^"]*)"`)
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return text
	}
	return text[:loc[2]] + value + text[loc[3]:]
}

// psq quotes a string for a single-quoted PowerShell literal.
func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// createShortcut makes a .lnk to this exe in a Windows special folder
// ("Desktop" or "Programs", the Start menu), pointing at the exe's own folder
// so config.toml and data/ are found.
func createShortcut(folder string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ps := "$d=[Environment]::GetFolderPath(" + psq(folder) + "); " +
		"$s=(New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $d 'openmini.lnk')); " +
		"$s.TargetPath=" + psq(exe) + "; $s.WorkingDirectory=" + psq(filepath.Dir(exe)) + "; " +
		"$s.IconLocation=" + psq(exe+",0") + "; $s.Description='openmini'; $s.Save()"
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	proc.Quiet(cmd)
	return cmd.Run()
}

// webLogin opens the visible browser and waits until the Gemini app shows an
// account. The profile folder keeps the session for headless runs.
func webLogin(cfg *config.Config) error {
	cfg.Web.Headless = false
	w := web.New(cfg.Web, cfg.Server.Timeout, func(string, ...any) {})
	fmt.Println("opening the browser window (first time downloads Chromium, about 150 MB)...")
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Close()
	if w.SignedIn() {
		fmt.Println("already signed in.")
		return nil
	}
	fmt.Println("sign in to Google in that window; this waits up to 15 minutes.")
	if err := w.WaitSignedIn(15 * time.Minute); err != nil {
		return err
	}
	fmt.Println("signed in; the session is saved in", cfg.Web.ProfileDir)
	time.Sleep(2 * time.Second) // let the page settle before closing
	return nil
}

// loginCmd signs in to the Gemini web app once.
func loginCmd() *gcli.Command {
	return &gcli.Command{
		Name: "login", Desc: "open a browser window to sign in to the Gemini web app once",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			return webLogin(cfg)
		},
	}
}

// setupCmd walks a new user through the two sign-ins and writes the config.
func setupCmd() *gcli.Command {
	return &gcli.Command{
		Name: "setup", Desc: "first run: install agy, sign in, choose the browser backend, write config.toml",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			in := bufio.NewReader(os.Stdin)
			if _, err := os.Stat(cfgPath); err != nil {
				if err := config.WriteTemplate(cfgPath); err != nil {
					return err
				}
				fmt.Println("wrote", cfgPath)
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			text, err := os.ReadFile(cfgPath)
			if err != nil {
				return err
			}
			body := string(text)

			// --- Antigravity (agy + agyapi) ---
			fmt.Println()
			fmt.Println("Antigravity backends (agy, agyapi) need Google's Antigravity CLI, agy.")
			installed, signedIn := agyState(cfg.Agy.Binary)
			if !installed && ask(in, "agy is not installed. Install it now with Google's installer?", true) {
				if err := installAgy(); err != nil {
					fmt.Println("installer failed:", err)
				}
				installed, signedIn = agyState(cfg.Agy.Binary)
			}
			if installed && !signedIn && ask(in, "agy is installed but not signed in. Sign in now? (agy opens; finish the sign-in, then quit it)", true) {
				bin := cfg.Agy.Binary
				if bin == "" {
					bin = "agy"
				}
				runInteractive(backend.FindAgy(bin))
				installed, signedIn = agyState(cfg.Agy.Binary)
			}
			useAg := installed && signedIn
			switch {
			case useAg:
				fmt.Println("agy: signed in. Backends agy and agyapi enabled.")
			case installed:
				fmt.Println("agy: not signed in. Backends agy and agyapi disabled; run `agy` to sign in, then `openmini setup` again.")
			default:
				fmt.Println("agy: not installed. Backends agy and agyapi disabled.")
			}
			body = setEnabled(setEnabled(body, "agy", useAg), "agyapi", useAg)

			// --- Gemini web app ---
			fmt.Println()
			useWeb := ask(in, "Use the Gemini web app backend too? (downloads a browser once and opens a sign-in window)", true)
			body = setEnabled(body, "web", useWeb)
			if useWeb {
				if err := webLogin(cfg); err != nil {
					fmt.Println("web sign-in did not complete:", err)
					fmt.Println("you can retry later with `openmini login`.")
				}
			}

			// requests without a backend prefix go to an enabled backend
			switch {
			case useWeb:
				body = setValue(body, "server", "default_backend", "web")
			case useAg:
				body = setValue(body, "server", "default_backend", "agyapi")
			}
			if err := os.WriteFile(cfgPath, []byte(body), 0644); err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				fmt.Println()
				if ask(in, "Create a desktop shortcut?", true) {
					if err := createShortcut("Desktop"); err != nil {
						fmt.Println("could not create the shortcut:", err)
					}
				}
				if ask(in, "Add openmini to the Start menu?", true) {
					if err := createShortcut("Programs"); err != nil {
						fmt.Println("could not add it to the Start menu:", err)
					}
				}
			}
			fmt.Println()
			fmt.Printf("done. Start it with %s\n", startHint())
			fmt.Printf("dashboard: http://localhost:%d/usage   API base: http://localhost:%d/v1\n", cfg.Server.Port, cfg.Server.Port)
			return nil
		},
	}
}
