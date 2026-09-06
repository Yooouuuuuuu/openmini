// openmini: consumer Google AI subscriptions as an OpenAI-compatible endpoint.
package main

import (
    "fmt"
    "io"
    "net/http"
    "os"
    "os/exec"
    "os/signal"
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

const version = "0.1.0"

var cfgPath string

func main() {
    app := gcli.NewApp(func(a *gcli.App) {
        a.Name = "openmini"
        a.Version = version
        a.Desc = "OpenAI-compatible endpoint over the Gemini web app and the Antigravity CLI"
    })
    app.Add(serveCmd(), usageCmd(), statusCmd(), doctorCmd(), initCmd())
    app.Run(nil)
}

func withConfigOpt(c *gcli.Command) {
    c.StrOpt(&cfgPath, "config", "c", "config.toml", "path to the configuration file")
}

func serveCmd() *gcli.Command {
    return &gcli.Command{
        Name: "serve", Desc: "start the HTTP server with the enabled backends",
        Config: withConfigOpt,
        Func: func(c *gcli.Command, _ []string) error {
            cfg, err := config.Load(cfgPath)
            if err != nil {
                return err
            }
            logger, err := logging.New(cfg.Log.Directory, cfg.Log.KeepDays)
            if err != nil {
                return err
            }
            logger.Printf("openmini %s starting", version)
            backends := map[string]backend.Backend{}
            var webDebug *web.Web
            if cfg.Web.Enabled {
                w := web.New(cfg.Web, cfg.Server.Timeout, logger.Printf)
                if err := w.Start(); err != nil {
                    return fmt.Errorf("web backend: %w", err)
                }
                defer w.Stop()
                backends["web"], webDebug = w, w
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
            sigs := make(chan os.Signal, 1)
            signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
            go func() {
                <-sigs
                logger.Printf("shutting down")
                if webDebug != nil {
                    webDebug.Stop()
                }
                os.Exit(0)
            }()
            logger.Printf("listening on :%d (base URL http://localhost:%d/v1), default backend %s", cfg.Server.Port, cfg.Server.Port, cfg.Server.DefaultBackend)
            return api.New(cfg, logger, backends, webDebug).Listen()
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

func usageCmd() *gcli.Command {
    return &gcli.Command{
        Name: "usage", Desc: "show remaining quota per backend (from the running server)",
        Config: withConfigOpt,
        Func: func(c *gcli.Command, _ []string) error {
            out, err := serverGet("/usage?format=text")
            if err != nil {
                return err
            }
            fmt.Print(out)
            return nil
        },
    }
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
            fmt.Printf("wrote %s; edit it, then run ./run.sh\n", cfgPath)
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
            _, tmuxErr := exec.LookPath("tmux")
            report(tmuxErr == nil, "tmux", "needed by run.sh")
            if cfg.Web.Enabled {
                info, err := os.Stat(cfg.Web.ProfileDir)
                if err != nil || !info.IsDir() {
                    report(false, "web profile", cfg.Web.ProfileDir+" missing: first run needs headless = false to sign in")
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
            if out, err := exec.Command("tailscale", "status", "--json").Output(); err == nil {
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
