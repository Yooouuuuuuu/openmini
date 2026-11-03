// Package setup checks what openmini needs on this machine (the agy CLI and
// its sign-in, the browser and the Gemini sign-in) and asks only about what
// is missing. openmini-setup.exe runs it; so do `openmini setup` and the
// first double-click of openmini.exe.
package setup

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"openmini/internal/backend"
	"openmini/internal/backend/web"
	"openmini/internal/config"
	"openmini/internal/proc"
)

// StartHint is how this platform starts the server.
func StartHint() string {
	if runtime.GOOS == "windows" {
		return "openmini.exe (double-click, or `openmini serve`)"
	}
	return "./run.sh (or `openmini serve`)"
}

// Ask prints a yes/no question and reads the answer; empty input means def.
func Ask(in *bufio.Reader, q string, def bool) bool {
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

// Run checks this machine and fixes what the user agrees to. cfgPath is the
// config to use (written from the template when missing); serverExe is the
// openmini executable the shortcuts point at and that is started at the end.
func Run(cfgPath, serverExe string) error {
	in := bufio.NewReader(os.Stdin)
	firstRun := false
	if _, err := os.Stat(cfgPath); err != nil {
		if err := config.WriteTemplate(cfgPath); err != nil {
			return err
		}
		firstRun = true
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

	// A running openmini holds the browser profile, so it has to stop before
	// the Gemini sign-in can be checked; it is offered a restart at the end.
	wasRunning, webCheckable := false, true
	if serverUp(cfg.Server.Port) {
		wasRunning = true
		fmt.Println()
		if Ask(in, "openmini is running and holds the browser profile. Stop it while setup checks the sign-ins?", true) {
			if err := stopServer(cfg.Server.Port); err != nil {
				fmt.Println("could not stop it:", err)
				webCheckable = false
			}
		} else {
			webCheckable = false
		}
	}

	// --- Antigravity (agy + agyapi) ---
	fmt.Println()
	fmt.Println("== Antigravity (backends agy and agyapi) ==")
	bin := cfg.Agy.Binary
	installed, signedIn := agyState(bin)
	switch {
	case installed && signedIn:
		fmt.Println("agy: installed and signed in.")
	case installed:
		fmt.Println("agy: installed, not signed in.")
	default:
		fmt.Println("agy: not installed.")
	}
	if !installed && Ask(in, "Install agy now with Google's installer?", true) {
		if err := installAgy(); err != nil {
			fmt.Println("installer failed:", err)
		}
		installed, signedIn = agyState(bin)
	}
	if installed && !signedIn && Ask(in, "Sign in to agy now? (agy opens; finish the sign-in, then quit it)", true) {
		runInteractive(backend.FindAgy(defaultBin(bin)))
		installed, signedIn = agyState(bin)
	}
	useAg := installed && signedIn
	if useAg {
		ensureAgent(defaultBin(bin), cfg.Agy.Agent)
	} else {
		fmt.Println("agy: not ready; backends agy and agyapi stay off. Run this setup again to finish.")
	}
	body = setEnabled(setEnabled(body, "agy", useAg), "agyapi", useAg)

	// --- Gemini web app ---
	fmt.Println()
	fmt.Println("== Gemini web app (backend web) ==")
	useWeb := false
	switch {
	case !webCheckable:
		useWeb = cfg.Web.Enabled
		fmt.Println("web: not checked while openmini is running; its setting is left as it is.")
	default:
		have := web.BrowserInstalled()
		if have {
			fmt.Println("browser: downloaded.")
		} else {
			fmt.Println("browser: not downloaded.")
			if Ask(in, "Download the browser for the web backend now? (about 150 MB, once)", true) {
				if err := web.InstallBrowser(); err != nil {
					fmt.Println("download failed:", err)
				}
				have = web.BrowserInstalled()
			}
		}
		if have {
			fmt.Println("checking the Gemini sign-in...")
			in2, err := web.CheckSignedIn(cfg.Web)
			switch {
			case err != nil:
				fmt.Println("could not open the browser:", err)
			case in2:
				fmt.Println("Gemini: signed in.")
				useWeb = true
			default:
				fmt.Println("Gemini: not signed in.")
				if Ask(in, "Sign in to the Gemini web app now? (a browser window opens)", true) {
					if err := WebLogin(cfg); err != nil {
						fmt.Println("sign-in did not complete:", err)
					} else {
						useWeb = true
					}
				}
			}
		}
		if !useWeb {
			fmt.Println("web: not ready; backend web stays off. Run this setup again to finish.")
		}
	}
	body = setEnabled(body, "web", useWeb)

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
		for _, sc := range []struct{ folder, q string }{
			{"Desktop", "Create a desktop shortcut?"},
			{"Programs", "Add openmini to the Start menu?"},
		} {
			if shortcutExists(sc.folder) {
				continue
			}
			fmt.Println()
			if Ask(in, sc.q, true) {
				if err := createShortcut(sc.folder, serverExe); err != nil {
					fmt.Println("could not create it:", err)
				}
			}
		}
	}

	fmt.Println()
	fmt.Printf("done: agy/agyapi %s, web %s.\n", onoff(useAg), onoff(useWeb))
	fmt.Printf("dashboard: http://localhost:%d/usage   API base: http://localhost:%d/v1\n", cfg.Server.Port, cfg.Server.Port)
	if runtime.GOOS == "windows" && !serverUp(cfg.Server.Port) && (firstRun || wasRunning) && (useAg || useWeb) {
		if Ask(in, "Start openmini now?", true) {
			return startServer(serverExe)
		}
	}
	fmt.Printf("start it with %s\n", StartHint())
	return nil
}

func onoff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func defaultBin(bin string) string {
	if bin == "" {
		return "agy"
	}
	return bin
}

// runInteractive runs a program with this terminal attached.
func runInteractive(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// agyState reports whether agy is installed and signed in.
func agyState(bin string) (installed, signedIn bool) {
	bin = backend.FindAgy(defaultBin(bin))
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

// storytellerAgent is the tool-less agent the agy backend runs by default.
// agy (1.4.7 on) refuses commandExecutionPolicy "never"; "off" is the value
// for "do not run commands", and with no tools nothing could run anyway.
const storytellerAgent = `---
name: storyteller
description: Text-only generation agent. No tools, no files, no commands; it only writes the requested text.
mainAgent: true
subagent: false
tools: []
skills: []
inheritMcp: false
permissionMode: plan
commandExecutionPolicy: off
---

# Core Instructions
You are a text generation model behind an API. You have no tools, no files and no shell.
Treat every message as the complete input and answer with the requested text only.
Do not describe plans, do not ask to use tools, do not add commentary about being an agent.
`

// agentListed reports whether agy lists a custom agent by that name.
func agentListed(bin, name string) bool {
	cmd := exec.Command(backend.FindAgy(bin), "agents")
	backend.PrepareAgy(cmd)
	cmd.Dir = os.TempDir()
	out, _ := cmd.Output()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == name {
			return true
		}
	}
	return false
}

// ensureAgent makes sure agy has the agent openmini's config names. agy fails
// every request when it is missing, so the storyteller agent is written (an
// existing file agy rejects is kept as agent.md.bak); another name is only
// reported.
func ensureAgent(bin, name string) {
	if name == "" {
		return
	}
	if agentListed(bin, name) {
		fmt.Printf("agy agent %q: present.\n", name)
		return
	}
	if name != "storyteller" {
		fmt.Printf("agy agent %q: agy does not list it; agy requests will fail until it exists (or set [agy] agent = \"\").\n", name)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("agy agent: no home folder:", err)
		return
	}
	dir := filepath.Join(home, ".gemini", "config", "agents", name)
	file := filepath.Join(dir, "agent.md")
	if _, err := os.Stat(file); err == nil {
		os.Rename(file, file+".bak")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Println("agy agent: could not create", dir+":", err)
		return
	}
	if err := os.WriteFile(file, []byte(storytellerAgent), 0644); err != nil {
		fmt.Println("agy agent: could not write", file+":", err)
		return
	}
	if agentListed(bin, name) {
		fmt.Printf("agy agent %q: written to %s.\n", name, file)
	} else {
		fmt.Printf("agy agent %q: written to %s, but agy still does not list it; agy requests may fail.\n", name, file)
	}
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

// shortcutExists reports whether openmini.lnk is already in that Windows
// special folder.
func shortcutExists(folder string) bool {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"Test-Path (Join-Path ([Environment]::GetFolderPath("+psq(folder)+")) 'openmini.lnk')")
	proc.Quiet(cmd)
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "True"
}

// createShortcut makes a .lnk to target in a Windows special folder
// ("Desktop" or "Programs", the Start menu), starting in target's folder so
// config.toml and data/ are found.
func createShortcut(folder, target string) error {
	ps := "$d=[Environment]::GetFolderPath(" + psq(folder) + "); " +
		"$s=(New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $d 'openmini.lnk')); " +
		"$s.TargetPath=" + psq(target) + "; $s.WorkingDirectory=" + psq(filepath.Dir(target)) + "; " +
		"$s.IconLocation=" + psq(target+",0") + "; $s.Description='openmini'; $s.Save()"
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	proc.Quiet(cmd)
	return cmd.Run()
}

// WebLogin opens the visible browser and waits until the Gemini app shows an
// account. The profile folder keeps the session for headless runs.
func WebLogin(cfg *config.Config) error {
	c := cfg.Web
	c.Headless = false
	w := web.New(c, cfg.Server.Timeout, func(string, ...any) {})
	fmt.Println("opening the browser window...")
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

// serverUp reports whether an openmini answers on this machine's port.
func serverUp(port int) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://localhost:%d/health", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// stopServer asks the running openmini to shut down and waits until it has.
func stopServer(port int) error {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Post(fmt.Sprintf("http://localhost:%d/shutdown", port), "text/plain", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	for i := 0; i < 40; i++ {
		if !serverUp(port) {
			time.Sleep(time.Second) // let the browser release the profile
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("still running after 20s")
}

// startServer starts openmini the way a double-click does; on Windows it
// opens its own window and this call returns.
func startServer(exe string) error {
	cmd := exec.Command(exe, "serve")
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
