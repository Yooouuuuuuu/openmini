// openmini: consumer Google AI subscriptions as an OpenAI-compatible endpoint.
package main

import (
	"openmini/internal/proc"
	"time"

	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/gookit/gcli/v3"

	"openmini/internal/api"
	"openmini/internal/backend"
	"openmini/internal/backend/agy"
	"openmini/internal/backend/agyapi"
	"openmini/internal/backend/web"
	"openmini/internal/config"
	"openmini/internal/logging"
)

const version = "0.2.0"

var cfgPath string

func main() {
	app := gcli.NewApp(func(a *gcli.App) {
		a.Name = "openmini"
		a.Version = version
		a.Desc = "OpenAI-compatible endpoint over the Gemini web app and the Antigravity CLI"
	})
	app.Add(serveCmd(), stopCmd(), setupCmd(), loginCmd(), statusCmd(), doctorCmd(), initCmd())
	keepConsoleAwake()
	plain := len(os.Args) == 1 // double-click on Windows, or plain "openmini"
	ran := ""
	if plain {
		if _, err := os.Stat("config.toml"); err != nil {
			ran = "setup" // first run: the wizard
		} else {
			ran = "serve"
		}
		os.Args = append(os.Args, ran)
	}
	app.ExitOnEnd = false
	code := app.Run(nil)
	if plain && runtime.GOOS == "windows" && !spawnedWindow { // a double-clicked console: say what happened and keep it open
		in := bufio.NewReader(os.Stdin)
		switch {
		case ran == "setup" && code == 0:
			fmt.Println()
			if ask(in, "Start openmini now?", true) {
				exe, _ := os.Executable()
				serve := exec.Command(exe, "serve")
				serve.Stdin, serve.Stdout, serve.Stderr = os.Stdin, os.Stdout, os.Stderr
				t0 := time.Now()
				serve.Run()
				if time.Since(t0) < 10*time.Second {
					fmt.Println("\nopenmini is running in its own window. This window can be closed.")
				} else {
					fmt.Println("\nopenmini has stopped.")
				}
			} else {
				fmt.Println("\nSetup is finished. Double-click openmini.exe whenever you want to start openmini.")
			}
		case ran == "serve":
			fmt.Println("\nopenmini has stopped.")
		}
		fmt.Print("Press Enter to close this window. ")
		in.ReadString('\n')
	}
	os.Exit(code)
}

func withConfigOpt(c *gcli.Command) {
	c.StrOpt(&cfgPath, "config", "c", "config.toml", "path to the configuration file")
}

var consoleMode bool
var spawnedWindow bool // this process handed over to a detached copy with its own window

func serveCmd() *gcli.Command {
	return &gcli.Command{
		Name: "serve", Desc: "start the HTTP server with the enabled backends",
		Config: func(c *gcli.Command) {
			withConfigOpt(c)
			c.BoolOpt(&consoleMode, "console", "", false, "Windows: stay in the console instead of opening openmini's own window")
		},
		Func: func(c *gcli.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			logger, err := logging.New(cfg.Log.Directory, cfg.Log.KeepDays)
			if err != nil {
				return err
			}
			if runtime.GOOS == "windows" && !consoleMode && os.Getenv("OPENMINI_WINDOW_CHILD") == "" {
				// Start again without a console and let this one go: the copy
				// that runs has only its own window (minimise hides it to the
				// tray, close stops it), and this terminal can close.
				if err := spawnDetached(cfg.Log.Directory); err == nil {
					spawnedWindow = true
					fmt.Println("openmini is running in its own window.")
					return nil
				}
			}
			logger.Printf("openmini %s starting", version)
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			if runtime.GOOS == "windows" && !consoleMode {
				url := fmt.Sprintf("http://localhost:%d/usage", cfg.Server.Port)
				if runWindowed("openmini", url, func() { sigs <- syscall.SIGTERM }, logger.SetSink, logger.Printf) {
					logger.Printf("running in openmini's own window: minimise hides it to the tray, close stops openmini")
				}
			}
			backends := map[string]backend.Backend{}
			var webDebug *web.Web
			if cfg.Web.Enabled {
				w := web.New(cfg.Web, cfg.Server.Timeout, logger.Printf)
				if err := w.Start(); err != nil {
					return fmt.Errorf("web backend: %w", err)
				}
				defer w.Stop()
				backends["web"], webDebug = w, w
				if n := cfg.Web.Lanes; n > 1 {
					if pool, err := web.NewPool(w, n); err != nil {
						logger.Printf("web: could not open %d lanes: %v (staying with one)", n, err)
					} else {
						backends["web"] = pool
						logger.Printf("web: %d lanes; requests overlap up to that many", pool.Lanes())
					}
				}
			}
			if cfg.Agy.Enabled {
				a := agy.New(cfg.Agy, cfg.Server.Timeout, logger.Printf)
				if err := a.Ready(); err != nil {
					logger.Printf("agy backend: %v (enabled, but not ready)", err)
				} else {
					logger.Printf("agy: %d models", len(a.Models()))
				}
				backends["agy"] = a
			}
			if cfg.AgyAPI.Enabled {
				x := agyapi.New(cfg.AgyAPI, cfg.Server.Timeout, logger.Printf)
				if err := x.Ready(); err != nil {
					logger.Printf("agyapi backend: %v (enabled, but not ready)", err)
				} else {
					logger.Printf("agyapi: %d models", len(x.Models()))
				}
				backends["agyapi"] = x
			}
			if len(backends) == 0 {
				return fmt.Errorf("no backend enabled in %s", cfgPath)
			}
			srv := api.New(cfg, logger, backends, webDebug)
			go func() {
				<-sigs
				logger.Printf("shutting down")
				srv.Shutdown() // stop what the backends are doing for requests nobody will read
				if webDebug != nil {
					webDebug.Stop()
				}
				closeWindow()
				os.Exit(0)
			}()
			logger.Printf("listening on :%d (base URL http://localhost:%d/v1), default backend %s", cfg.Server.Port, cfg.Server.Port, cfg.Server.DefaultBackend)
			srv.OnStop = func() { sigs <- syscall.SIGTERM } // "openmini stop" takes the same path as Ctrl+C
			return srv.Listen()
		},
	}
}

func serverGet(path string) (string, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d%s", cfg.Server.Port, path), nil)
	if len(cfg.Server.APIKeys) > 0 {
		req.Header.Set("Authorization", "Bearer "+cfg.Server.APIKeys[0])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("is openmini running? %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func statusCmd() *gcli.Command {
	return &gcli.Command{
		Name: "status", Desc: "show active and recent requests (from the running server)",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			out, err := serverGet("/status?format=text")
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
}

func initCmd() *gcli.Command {
	return &gcli.Command{
		Name: "init", Desc: "write config.toml from the template if it does not exist",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			if _, err := os.Stat(cfgPath); err == nil {
				fmt.Printf("%s already exists\n", cfgPath)
				return nil
			}
			if err := config.WriteTemplate(cfgPath); err != nil {
				return err
			}
			fmt.Printf("wrote %s; edit it, then run %s\n", cfgPath, startHint())
			return nil
		},
	}
}

// stopCmd asks the running server on this machine to shut down.
func stopCmd() *gcli.Command {
	return &gcli.Command{
		Name: "stop", Desc: "stop the running openmini on this machine",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			req, _ := http.NewRequest("POST", fmt.Sprintf("http://localhost:%d/shutdown", cfg.Server.Port), nil)
			if len(cfg.Server.APIKeys) > 0 {
				req.Header.Set("Authorization", "Bearer "+cfg.Server.APIKeys[0])
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Println("openmini is not running")
				return nil
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("server answered %s", resp.Status)
			}
			fmt.Println("stopping openmini")
			return nil
		},
	}
}

// doctorCmd checks the environment without starting the server.
func doctorCmd() *gcli.Command {
	return &gcli.Command{
		Name: "doctor", Desc: "check config, tools, sign-in state and ports",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			ok := true
			report := func(good bool, what, detail string) {
				mark := "ok  "
				if !good {
					mark, ok = "FAIL", false
				}
				fmt.Printf("[%s] %-22s %s\n", mark, what, detail)
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				report(false, "config", err.Error())
				return fmt.Errorf("doctor found problems")
			}
			report(true, "config", cfgPath)
			if runtime.GOOS != "windows" {
				_, tmuxErr := exec.LookPath("tmux")
				report(tmuxErr == nil, "tmux", "needed by run.sh")
			}
			if cfg.Web.Enabled {
				info, err := os.Stat(cfg.Web.ProfileDir)
				if err != nil || !info.IsDir() {
					report(false, "web profile", cfg.Web.ProfileDir+" missing: run `openmini login` to sign in once")
				} else {
					report(true, "web profile", cfg.Web.ProfileDir)
				}
			}
			if cfg.Agy.Enabled {
				a := agy.New(cfg.Agy, 0, func(string, ...any) {})
				if err := a.Ready(); err != nil {
					report(false, "agy", err.Error())
				} else {
					report(true, "agy", fmt.Sprintf("signed in, %d models", len(a.Models())))
				}
			}
			if cfg.AgyAPI.Enabled {
				x := agyapi.New(cfg.AgyAPI, 0, func(string, ...any) {})
				if err := x.Ready(); err != nil {
					report(false, "agyapi", err.Error())
				} else {
					report(true, "agyapi", fmt.Sprintf("token ok, project resolved, %d models", len(x.Models())))
				}
			}
			if resp, err := http.Get(fmt.Sprintf("http://localhost:%d/health", cfg.Server.Port)); err == nil {
				resp.Body.Close()
				report(true, "port", fmt.Sprintf("%d already answers: a server is running", cfg.Server.Port))
			} else {
				report(true, "port", fmt.Sprintf("%d free", cfg.Server.Port))
			}
			ts := exec.Command("tailscale", "status", "--json")
			proc.Quiet(ts)
			if out, err := ts.Output(); err == nil {
				if i := strings.Index(string(out), `"DNSName"`); i > 0 {
					rest := string(out)[i+len(`"DNSName"`):]
					if q := strings.Index(rest, `"`); q >= 0 {
						rest = rest[q+1:]
						if e := strings.Index(rest, `"`); e > 0 {
							report(true, "tailscale", "reachable as http://"+strings.TrimSuffix(rest[:e], ".")+fmt.Sprintf(":%d", cfg.Server.Port))
						}
					}
				}
			}
			if !ok {
				return fmt.Errorf("doctor found problems")
			}
			return nil
		},
	}
}
