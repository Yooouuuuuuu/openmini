// Package agy runs the Antigravity CLI in headless stream-json mode, one
// process per request, with a tool-less custom agent.
package agy

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "syscall"
    "time"

    "openmini/internal/backend"
    "openmini/internal/config"
)

type Agy struct {
    cfg     config.Agy
    timeout int
    logf    func(string, ...any)
    models  []string
    names   map[string]string
    sem     chan struct{}
}

func New(cfg config.Agy, timeoutSec int, logf func(string, ...any)) *Agy {
    if strings.HasPrefix(cfg.Binary, "~/") {
        home, _ := os.UserHomeDir()
        cfg.Binary = filepath.Join(home, cfg.Binary[2:])
    }
    os.MkdirAll(cfg.Workspace, 0755)
    return &Agy{cfg: cfg, timeout: timeoutSec, logf: logf, names: map[string]string{}, sem: make(chan struct{}, cfg.Parallel)}
}

func (a *Agy) Name() string       { return "agy" }
func (a *Agy) StableStream() bool { return true }
func (a *Agy) Models() []string   { return a.models }

// Ready runs `agy models`, which fails when agy is not signed in.
func (a *Agy) Ready() error {
    out, err := exec.Command(a.cfg.Binary, "models").Output()
    if err != nil {
        return fmt.Errorf("agy models: %v (is agy installed and signed in?)", err)
    }
    a.models = a.models[:0]
    for _, line := range strings.Split(string(out), "\n") {
        parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
        if len(parts) == 2 && !strings.Contains(parts[0], " ") {
            a.models = append(a.models, parts[0])
            a.names[parts[0]] = parts[1]
        }
    }
    if len(a.models) == 0 {
        return fmt.Errorf("agy listed no models; sign in with `agy` first")
    }
    return nil
}

type result struct {
    Status   string         `json:"status"`
    Response string         `json:"response"`
    Error    string         `json:"error"`
    Usage    map[string]int `json:"usage"`
}

func (a *Agy) Complete(c backend.Call) (backend.Result, error) {
    a.sem <- struct{}{}
    defer func() { <-a.sem }()

    model := c.Model
    if model == "" {
        model = a.cfg.Model
    }
    args := []string{"--input-format", "stream-json", "--output-format", "stream-json"}
    if a.cfg.Agent != "" {
        args = append(args, "--agent", a.cfg.Agent)
    }
    if a.cfg.Mode != "" {
        args = append(args, "--mode", a.cfg.Mode)
    }
    if model != "" {
        args = append(args, "--model", model)
    }
    if a.cfg.Effort != "" {
        args = append(args, "--effort", a.cfg.Effort)
    }
    if a.timeout > 0 {
        args = append(args, "--print-timeout", fmt.Sprintf("%ds", a.timeout))
    } else {
        args = append(args, "--print-timeout", "24h")
    }
    // --print must come last with an empty value: given earlier it swallows the next flag.
    args = append(args, "--print=")

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    cmd := exec.CommandContext(ctx, a.cfg.Binary, args...)
    cmd.Dir = a.cfg.Workspace
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    stdin, err := cmd.StdinPipe()
    if err != nil {
        return backend.Result{}, err
    }
    stdout, err := cmd.StdoutPipe()
    if err != nil {
        return backend.Result{}, err
    }
    var stderr strings.Builder
    cmd.Stderr = &stderr
    if err := cmd.Start(); err != nil {
        return backend.Result{}, fmt.Errorf("start agy: %v", err)
    }
    defer func() {
        // stream-json mode never exits on its own; stop the whole process group
        syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
        time.AfterFunc(3*time.Second, func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
        cmd.Wait()
    }()

    ev, _ := json.Marshal(map[string]any{"event": "user", "message": map[string]string{"content": c.Prompt}})
    if _, err := stdin.Write(append(ev, '\n')); err != nil {
        return backend.Result{}, fmt.Errorf("write prompt: %v", err)
    }
    if c.OnPhase != nil {
        c.OnPhase("submitted", "")
    }

    var res result
    got, generating := false, false
    var soFar strings.Builder
    sc := bufio.NewScanner(stdout)
    sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
    for sc.Scan() {
        var e struct {
            Event string `json:"event"`
            Step  struct {
                Type  string `json:"step_type"`
                Delta string `json:"text_delta"`
            } `json:"step_update"`
            Result result `json:"result"`
        }
        if json.Unmarshal(sc.Bytes(), &e) != nil {
            continue
        }
        switch e.Event {
        case "step_update":
            if e.Step.Type == "agent_response" && e.Step.Delta != "" {
                if !generating {
                    generating = true
                    if c.OnPhase != nil {
                        c.OnPhase("generating", "")
                    }
                }
                soFar.WriteString(e.Step.Delta)
                if c.OnText != nil {
                    c.OnText(soFar.String())
                }
            }
        case "result":
            res, got = e.Result, true
        }
        if got {
            break
        }
    }
    stdin.Close()
    if s := strings.TrimSpace(stderr.String()); s != "" {
        a.logf("agy stderr (%s): %s", c.ID, firstLine(s))
    }
    if !got {
        msg := strings.TrimSpace(stderr.String())
        if msg == "" {
            msg = "agy ended without a result event"
        }
        return backend.Result{Text: soFar.String()}, fmt.Errorf("%s", firstLine(msg))
    }
    text := res.Response
    if res.Status != "SUCCESS" {
        text = fmt.Sprintf("[openmini/agy] status %s: %s", res.Status, res.Error)
        if res.Response != "" {
            text = res.Response + "\n" + text
        }
    }
    return backend.Result{Text: text, Status: res.Status, Usage: res.Usage}, nil
}

// Usage runs agy's /usage command and returns the parsed rows.
func (a *Agy) Usage() (any, error) {
    cmd := exec.Command(a.cfg.Binary, "--print", "/usage", "--output-format", "text")
    cmd.Dir = a.cfg.Workspace
    out, err := cmd.CombinedOutput()
    if err != nil && len(out) == 0 {
        return nil, err
    }
    type row struct {
        Pool      string `json:"pool"`
        Window    string `json:"window"`
        Remaining string `json:"remaining"`
        ResetsAt  string `json:"resets_at,omitempty"`
        ResetsIn  string `json:"resets_in,omitempty"`
    }
    var rows []row
    for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
        f := strings.Split(line, "\t")
        if len(f) >= 3 {
            r := row{Pool: f[0], Window: f[1], Remaining: f[2]}
            if len(f) >= 4 {
                r.ResetsAt = f[3]
                if t, err := time.Parse(time.RFC3339, f[3]); err == nil {
                    r.ResetsIn = time.Until(t).Round(time.Minute).String()
                }
            }
            rows = append(rows, r)
        }
    }
    if len(rows) == 0 {
        return nil, fmt.Errorf("unexpected /usage output: %s", firstLine(string(out)))
    }
    return rows, nil
}

func firstLine(s string) string {
    if i := strings.IndexByte(s, '\n'); i >= 0 {
        return s[:i]
    }
    return s
}
