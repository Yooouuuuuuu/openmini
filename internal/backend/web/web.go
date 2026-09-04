// Package web drives gemini.google.com in a signed-in browser through
// Playwright: a fresh chat per request, the prompt pasted, the reply read from
// Gemini's copy button.
package web

import (
    "fmt"
    "path/filepath"
    "regexp"
    "strings"
    "sync"
    "time"
    "unicode/utf8"

    md "github.com/JohannesKaufmann/html-to-markdown"
    "github.com/JohannesKaufmann/html-to-markdown/plugin"
    pw "github.com/mxschmitt/playwright-go"

    "openmini/internal/backend"
    "openmini/internal/config"
)

const (
    geminiURL     = "https://gemini.google.com/app"
    promptBoxSel  = "div.ql-editor[role='textbox']" // Quill editor; the role avoids the localized label
    replySel      = "model-response"
    replyTextSel  = "div.markdown-main-panel"
    busyIconSel   = "[data-mat-icon-name='stop']"
    modelLabelSel = "[data-test-id='logo-pill-label-container'] .picker-primary-text"
    signedInSel   = "[gem-open-account-menu]"

    deadReplyAfter = 45 * time.Second

    toolsInstruction = "Answer directly in plain Markdown. Do not use web search, code execution, " +
        "image generation, or any other tools unless the user explicitly asks for them, and do not emit tool calls."

    attachHintWithMessage = "The attached file %s contains the instructions and the conversation so far. " +
        "Follow them exactly, then reply to the message below. Output only the reply.\n\n"
    attachHintAlone = "The attached file %s contains the instructions and the conversation. " +
        "Follow them exactly and reply to the last User message in it. Output only the reply."
    attachOverflowHint = "This message is in two parts: the text below, and its direct continuation in the attached file %s. " +
        "Read both, in that order, before replying.\n\n"
    splitPartHint = "This is part %d of %d of a long message. Do not act on it yet; more parts follow. " +
        "Reply with only the word OK.\n\n"
    splitFinalHint = "That was the last part. All parts together (%d of them) are the instructions and the conversation so far. " +
        "Follow them exactly, then reply to the message below. Output only the reply.\n\n"
    splitFinalAlone = "That was the last part. All parts together (%d of them) are the instructions and the conversation. " +
        "Follow them exactly and reply to the last User message in them. Output only the reply."
)

// ToolsInstruction is exposed for the API layer's discourage_tools option.
const ToolsInstruction = toolsInstruction

// cleanReplyJS returns plain HTML for the reply element: Gemini's code and
// table wrappers become <pre><code> and <table>, buttons and labels go, and
// stale duplicate render nodes are dropped.
const cleanReplyJS = `el => {
  const c = el.cloneNode(true);
  c.querySelectorAll('code-block').forEach(cb => {
    const label = cb.querySelector('.code-block-decoration span');
    const lang = label ? label.textContent.trim().toLowerCase() : '';
    const src = cb.querySelector('code[data-test-id="code-content"], pre code, pre');
    const pre = document.createElement('pre');
    const code = document.createElement('code');
    if (lang) code.className = 'language-' + lang;
    code.textContent = (src ? src.textContent : '').replace(/\s+$/, '');
    pre.appendChild(code);
    cb.replaceWith(pre);
  });
  c.querySelectorAll('table-block').forEach(tb => {
    const t = tb.querySelector('table');
    if (t) tb.replaceWith(t); else tb.remove();
  });
  c.querySelectorAll('gem-icon-button, button, gem-popover, mat-icon, .code-block-decoration, [role="tooltip"], sources-list, source-footnote')
    .forEach(n => n.remove());
  const seen = new Map();
  c.querySelectorAll('[data-path-to-node]').forEach(n => seen.set(n.getAttribute('data-path-to-node'), n));
  c.querySelectorAll('[data-path-to-node]').forEach(n => { const k = seen.get(n.getAttribute('data-path-to-node')); if (k !== n && !n.contains(k)) n.remove(); });
  c.querySelectorAll('li').forEach(li => {
    const kids = Array.from(li.childNodes).filter(n => n.nodeType !== 3 || n.textContent.trim());
    if (kids.length === 1 && kids[0].tagName === 'P') li.replaceChildren(...kids[0].childNodes);
  });
  return c.innerHTML;
}`

var (
    tripleNewline = regexp.MustCompile(`\n{3,}`)
    // geminiErrorRe matches Gemini's short failure notices and refusals.
    geminiErrorRe = regexp.MustCompile(`(?i)(something went wrong|encounter(ed|ing) an error|try (your request )?again|having a hard time fulfilling|help you with something else|發生錯誤|出了點問題|請再試一次` +
        `|as a language model|I can't help with that|I'm unable to help|語言模型|语言模型|没法办到|沒法辦到|没办法提供|沒辦法提供|无法提供这方面|無法提供這方面` +
        `|只会生成文本|只會生成文字|文本 ?AI|文字 ?AI|超出了我的程序|超出了我的程式|没法帮到你|沒法幫到你|无法帮助你|無法幫助你)`)
    errDeadReply = fmt.Errorf("dead reply: Gemini opened a reply but produced no text")
)

type Web struct {
    cfg     config.Web
    timeout int
    logf    func(string, ...any)

    mu         sync.Mutex
    pwInstance *pw.Playwright
    ctx        pw.BrowserContext
    page       pw.Page
    mdConv     *md.Converter
    started    bool
    signedIn   bool

    modelOptions []string
    currentModel string
    desiredModel string
}

func New(cfg config.Web, timeoutSec int, logf func(string, ...any)) *Web {
    w := &Web{cfg: cfg, timeout: timeoutSec, logf: logf}
    w.mdConv = md.NewConverter("", true, &md.Options{HeadingStyle: "atx", CodeBlockStyle: "fenced", Fence: "```", EmDelimiter: "*", StrongDelimiter: "**", EscapeMode: "disabled"})
    w.mdConv.Use(plugin.Table())
    return w
}

func (w *Web) Name() string       { return "web" }
func (w *Web) StableStream() bool { return false }
func (w *Web) Models() []string   { return w.modelOptions }

func (w *Web) Ready() error {
    if !w.started {
        return fmt.Errorf("browser not started")
    }
    if !w.signedIn {
        return fmt.Errorf("browser is not signed in to Google; start with headless = false and sign in once")
    }
    return nil
}

func (w *Web) Usage() (any, error) {
    return map[string]string{"note": "the Gemini app exposes no usage counter; a limit shows up as a refusal or as a reply that never starts"}, nil
}

// Start launches the browser with the persistent profile and opens Gemini.
func (w *Web) Start() error {
    var err error
    if err = pw.Install(&pw.RunOptions{Browsers: []string{"chromium"}}); err != nil {
        return fmt.Errorf("install playwright: %v", err)
    }
    if w.pwInstance, err = pw.Run(); err != nil {
        return fmt.Errorf("init playwright: %v", err)
    }
    profileDir, err := filepath.Abs(w.cfg.ProfileDir)
    if err != nil {
        return err
    }
    w.ctx, err = w.pwInstance.Chromium.LaunchPersistentContext(profileDir, pw.BrowserTypeLaunchPersistentContextOptions{
        Headless: pw.Bool(w.cfg.Headless),
        Channel:  pw.String("chromium"), // full Chromium in both modes
        Args:     []string{"--disable-blink-features=AutomationControlled"},
    })
    if err != nil {
        return fmt.Errorf("launch browser: %v", err)
    }
    if pages := w.ctx.Pages(); len(pages) > 0 {
        w.page = pages[0]
    } else if w.page, err = w.ctx.NewPage(); err != nil {
        return fmt.Errorf("new page: %v", err)
    }
    if _, err = w.page.Goto(geminiURL, pw.PageGotoOptions{WaitUntil: pw.WaitUntilStateDomcontentloaded}); err != nil {
        return fmt.Errorf("goto gemini: %v", err)
    }
    time.Sleep(2 * time.Second)
    w.started = true
    w.signedIn = w.exists(w.page.Locator(signedInSel))
    if !w.signedIn {
        w.logf("web: not signed in; profile %s. Set headless = false, restart, sign in once in the window.", profileDir)
        return nil
    }
    if labels, sel, err := w.readModelMenu(); err == nil {
        w.modelOptions, w.currentModel = labels, sel
        w.logf("web: signed in; model picker %v, selected %q", labels, sel)
    } else {
        w.logf("web: signed in; picker shows %q (menu not readable: %v)", w.currentModelLabel(), err)
    }
    if err := w.ensureModel(w.cfg.Model); err != nil {
        w.logf("web: warning: %v", err)
    }
    return nil
}

// Stop closes the browser cleanly.
func (w *Web) Stop() {
    if w.ctx != nil {
        w.ctx.Close()
    }
    if w.pwInstance != nil {
        w.pwInstance.Stop()
    }
}

// HTML returns the current page markup (for fixing selectors).
func (w *Web) HTML() (string, error) { return w.page.Content() }

// Screenshot returns a PNG of the current page.
func (w *Web) Screenshot() ([]byte, error) {
    return w.page.Screenshot(pw.PageScreenshotOptions{FullPage: pw.Bool(true)})
}

func (w *Web) exists(l pw.Locator) bool {
    n, err := l.Count()
    return err == nil && n > 0
}

func (w *Web) waitMillis() *float64 { return pw.Float(float64(w.timeout) * 1000) }

func (w *Web) deadlinePassed(start time.Time) bool {
    return w.timeout > 0 && time.Since(start) > time.Duration(w.timeout)*time.Second
}

func (w *Web) currentModelLabel() string {
    l := w.page.Locator(modelLabelSel).First()
    if !w.exists(l) {
        return ""
    }
    t, err := l.TextContent(pw.LocatorTextContentOptions{Timeout: pw.Float(2000)})
    if err != nil {
        return ""
    }
    return strings.TrimSpace(t)
}

// ---------------------------------------------------------------------------
// Model picker
// ---------------------------------------------------------------------------

func (w *Web) openModelMenu() error {
    if err := w.page.Locator("[data-test-id='bard-mode-menu-button']").First().Click(pw.LocatorClickOptions{Timeout: pw.Float(10000)}); err != nil {
        return fmt.Errorf("model picker: %v", err)
    }
    if err := w.page.Locator("[role='menuitem'] span.label").First().WaitFor(pw.LocatorWaitForOptions{State: pw.WaitForSelectorStateVisible, Timeout: pw.Float(5000)}); err != nil {
        w.page.Keyboard().Press("Escape")
        return fmt.Errorf("model menu did not open: %v", err)
    }
    return nil
}

func (w *Web) readModelMenu() ([]string, string, error) {
    if err := w.openModelMenu(); err != nil {
        return nil, "", err
    }
    defer w.page.Keyboard().Press("Escape")
    items := w.page.Locator("[role='menuitem']")
    n, _ := items.Count()
    var labels []string
    selected := ""
    for i := 0; i < n; i++ {
        it := items.Nth(i)
        lab, err := it.Locator("span.label").First().TextContent(pw.LocatorTextContentOptions{Timeout: pw.Float(2000)})
        if err != nil || strings.TrimSpace(lab) == "" {
            continue
        }
        lab = strings.TrimSpace(lab)
        labels = append(labels, lab)
        if w.exists(it.Locator("gem-menu-item-content.selected")) {
            selected = lab
        }
    }
    return labels, selected, nil
}

func (w *Web) ensureModel(want string) error {
    if want == "" || want == w.currentModel {
        return nil
    }
    if err := w.openModelMenu(); err != nil {
        return err
    }
    item := w.page.Locator("[role='menuitem']", pw.PageLocatorOptions{HasText: want})
    if !w.exists(item) {
        w.page.Keyboard().Press("Escape")
        return fmt.Errorf("model %q is not in the picker (entries: %v)", want, w.modelOptions)
    }
    if err := item.First().Click(pw.LocatorClickOptions{Timeout: pw.Float(5000)}); err != nil {
        w.page.Keyboard().Press("Escape")
        return fmt.Errorf("selecting model %q: %v", want, err)
    }
    time.Sleep(800 * time.Millisecond)
    w.currentModel = want
    w.logf("web: model picker switched to %q", want)
    return nil
}

// ---------------------------------------------------------------------------
// Chat mechanics
// ---------------------------------------------------------------------------

func (w *Web) newChat() error {
    t0 := time.Now()
    if _, err := w.page.Goto(geminiURL, pw.PageGotoOptions{WaitUntil: pw.WaitUntilStateDomcontentloaded}); err != nil {
        return fmt.Errorf("open new chat: %v", err)
    }
    box := w.page.Locator(promptBoxSel)
    if err := box.WaitFor(pw.LocatorWaitForOptions{State: pw.WaitForSelectorStateVisible, Timeout: w.waitMillis()}); err != nil {
        return fmt.Errorf("prompt box did not appear; is the browser signed in? (%v)", err)
    }
    w.logf("web: new chat ready in %.1fs", time.Since(t0).Seconds())
    return w.ensureModel(w.desiredModel)
}

func (w *Web) attachText(name, text string) error {
    menu := w.page.Locator("gem-icon-button.menu-button button").First()
    if err := menu.Click(); err != nil {
        return fmt.Errorf("open upload menu: %v", err)
    }
    input := w.page.Locator("input.hidden-file-input").First()
    if err := input.WaitFor(pw.LocatorWaitForOptions{State: pw.WaitForSelectorStateAttached, Timeout: w.waitMillis()}); err != nil {
        return fmt.Errorf("file input did not appear: %v", err)
    }
    if err := input.SetInputFiles([]pw.InputFile{{Name: name, MimeType: "text/plain", Buffer: []byte(text)}}); err != nil {
        return fmt.Errorf("set file: %v", err)
    }
    return nil
}

func (w *Web) waitForUpload() error {
    chip := w.page.Locator("uploader-file-preview").First()
    if err := chip.WaitFor(pw.LocatorWaitForOptions{State: pw.WaitForSelectorStateAttached, Timeout: w.waitMillis()}); err != nil {
        return fmt.Errorf("attachment chip did not appear: %v", err)
    }
    busy := w.page.Locator("uploader-file-preview-container mat-progress-spinner, uploader-file-preview-container mat-progress-bar, uploader-file-preview-container [class*='uploading'], uploader-file-preview-container [class*='loading']")
    start := time.Now()
    for w.exists(busy) {
        if w.deadlinePassed(start) {
            return fmt.Errorf("attachment upload did not finish within %ds", w.timeout)
        }
        time.Sleep(200 * time.Millisecond)
    }
    time.Sleep(800 * time.Millisecond)
    return nil
}

func splitChunks(text string, size int) []string {
    var out []string
    for utf8.RuneCountInString(text) > size {
        end := 0
        for i := 0; i < size; i++ {
            _, n := utf8.DecodeRuneInString(text[end:])
            end += n
        }
        cut := strings.LastIndex(text[:end], "\n")
        if cut < end/2 {
            cut = end
        }
        out = append(out, text[:cut])
        text = text[cut:]
    }
    return append(out, text)
}

// Complete delivers one prompt in a fresh chat, retrying on Gemini's own
// error notices when retries are configured. The last reply is passed through
// as-is; only a reply with no text at all is an error.
func (w *Web) Complete(c backend.Call) (backend.Result, error) {
    w.mu.Lock()
    defer w.mu.Unlock()
    if err := w.Ready(); err != nil {
        return backend.Result{}, err
    }
    w.desiredModel = c.Model
    if w.desiredModel == "" {
        w.desiredModel = w.cfg.Model
    }
    attempts := 1 + w.cfg.Retries
    for i := 1; ; i++ {
        text, err := w.ask(c)
        if err == nil && (i >= attempts || !isGeminiError(text)) {
            return backend.Result{Text: text, Status: "SUCCESS"}, nil
        }
        if err != nil && err != errDeadReply {
            return backend.Result{Text: text}, err
        }
        if i >= attempts {
            return backend.Result{}, err
        }
        if err != nil {
            w.logf("web: dead reply on attempt %d/%d (%s); retrying", i, attempts, c.ID)
        } else {
            w.logf("web: Gemini notice on attempt %d/%d (%s); retrying", i, attempts, c.ID)
        }
        time.Sleep(time.Duration(2*i) * time.Second)
    }
}

func isGeminiError(reply string) bool {
    return len(reply) < 200 && geminiErrorRe.MatchString(reply)
}

func (w *Web) ask(c backend.Call) (string, error) {
    if err := w.newChat(); err != nil {
        return "", err
    }
    limit := w.cfg.MaxInlineChars
    if backend.Chars(c.Prompt) <= limit || w.cfg.LongPromptMode == "paste" {
        return w.sendAndWait(c.Prompt, c)
    }
    body, final := c.Prompt, ""
    if c.LastUser != "" && backend.Chars(c.LastUser)+400 <= limit && c.Context != "" {
        body, final = c.Context, c.LastUser
    }
    if w.cfg.LongPromptMode == "split" {
        chunks := splitChunks(body, limit-300)
        w.logf("web: %s prompt is %d chars; sending %d parts before the final message", c.ID, backend.Chars(c.Prompt), len(chunks))
        for i, ch := range chunks {
            if _, err := w.sendAndWait(fmt.Sprintf(splitPartHint, i+1, len(chunks))+ch, backend.Call{ID: c.ID}); err != nil {
                return "", fmt.Errorf("part %d/%d: %v", i+1, len(chunks), err)
            }
        }
        if final == "" {
            return w.sendAndWait(fmt.Sprintf(splitFinalAlone, len(chunks)), c)
        }
        return w.sendAndWait(fmt.Sprintf(splitFinalHint, len(chunks))+final, c)
    }
    // attach
    name := w.cfg.AttachName
    boxText, fileText := "", ""
    if w.cfg.AttachFillBox {
        hint := fmt.Sprintf(attachOverflowHint, name)
        parts := splitChunks(c.Prompt, limit-backend.Chars(hint)-50)
        boxText, fileText = hint+parts[0], strings.Join(parts[1:], "")
    } else if final == "" {
        boxText, fileText = fmt.Sprintf(attachHintAlone, name), body
    } else {
        boxText, fileText = fmt.Sprintf(attachHintWithMessage, name)+final, body
    }
    w.logf("web: %s prompt is %d chars; box %d, attaching %d as %s", c.ID, backend.Chars(c.Prompt), backend.Chars(boxText), backend.Chars(fileText), name)
    if err := w.attachText(name, fileText); err != nil {
        return "", err
    }
    if err := w.waitForUpload(); err != nil {
        return "", err
    }
    return w.sendAndWait(boxText, c)
}

// sendAndWait puts text into the box, submits, and returns the finished reply.
func (w *Web) sendAndWait(text string, c backend.Call) (string, error) {
    t0 := time.Now()
    box := w.page.Locator(promptBoxSel)
    if err := box.Click(); err != nil {
        return "", fmt.Errorf("prompt box not found: %v", err)
    }
    if w.cfg.InputMethod == "paste" {
        if err := w.ctx.GrantPermissions([]string{"clipboard-read", "clipboard-write"}); err != nil {
            return "", fmt.Errorf("clipboard permission: %v", err)
        }
        if _, err := w.page.Evaluate(`t => navigator.clipboard.writeText(t)`, text); err != nil {
            return "", fmt.Errorf("clipboard write: %v", err)
        }
        if err := w.page.Keyboard().Press("Control+V"); err != nil {
            return "", fmt.Errorf("paste: %v", err)
        }
        time.Sleep(500 * time.Millisecond)
    } else if err := box.Fill(text); err != nil {
        return "", fmt.Errorf("typing prompt: %v", err)
    }
    if raw, err := box.Evaluate(`el => el.innerText.length`, nil, pw.LocatorEvaluateOptions{Timeout: pw.Float(5000)}); err == nil {
        if kept, ok := raw.(float64); ok {
            if kept == 0 && backend.Chars(text) > 0 {
                return "", fmt.Errorf("the page rejected the text: the prompt box is empty after %s (a single line over ~32k characters is refused)", w.cfg.InputMethod)
            }
            if kept < float64(backend.Chars(text))*0.95 {
                w.logf("web: warning: the prompt box kept %d of %d characters", int(kept), backend.Chars(text))
            }
        }
    }
    responses := w.page.Locator(replySel)
    before, _ := responses.Count()
    if err := box.Press("Enter"); err != nil {
        return "", fmt.Errorf("sending prompt: %v", err)
    }
    w.logf("web: %s typed in %.1fs (%d chars)", c.ID, time.Since(t0).Seconds(), backend.Chars(text))
    if c.OnPhase != nil {
        c.OnPhase("submitted", "")
    }
    start := time.Now()

    for {
        if n, _ := responses.Count(); n > before {
            break
        }
        if w.deadlinePassed(start) {
            return "", fmt.Errorf("timeout: Gemini did not start a reply within %ds", w.timeout)
        }
        if st := w.cfg.StartTimeout; st > 0 && time.Since(start) > time.Duration(st)*time.Second {
            if msg := w.pageNotice(); msg != "" {
                return "", fmt.Errorf("no reply started within %ds; the page says: %s", st, msg)
            }
            return "", fmt.Errorf("no reply started within %ds after submitting", st)
        }
        time.Sleep(250 * time.Millisecond)
    }
    reply := responses.Last().Locator(replyTextSel).Last()
    w.logf("web: %s reply container after %.1fs", c.ID, time.Since(t0).Seconds())
    if c.OnPhase != nil {
        c.OnPhase("generating", "")
    }

    last := ""
    lastChange := time.Now()
    short := pw.Float(2000)
    for {
        if w.exists(reply) {
            if raw, err := reply.Evaluate(cleanReplyJS, nil, pw.LocatorEvaluateOptions{Timeout: short}); err == nil {
                if html, ok := raw.(string); ok {
                    txt := w.markdownOf(html)
                    if txt != last {
                        last, lastChange = txt, time.Now()
                        if c.OnText != nil {
                            c.OnText(txt)
                        }
                    }
                }
            }
            busy, _ := reply.GetAttribute("aria-busy", pw.LocatorGetAttributeOptions{Timeout: short})
            generating := w.exists(w.page.Locator(busyIconSel))
            if busy != "true" && !generating && last != "" && time.Since(lastChange) > 500*time.Millisecond {
                w.logf("web: %s reply finished after %.1fs (%d chars)", c.ID, time.Since(t0).Seconds(), backend.Chars(last))
                if w.cfg.ReplySource == "copy" {
                    if t, err := w.copyReply(); err == nil {
                        return t, nil
                    } else {
                        w.logf("web: copy button failed (%v); using the rendered reply", err)
                    }
                }
                return last, nil
            }
            if busy == "true" && !generating && last == "" && time.Since(start) > deadReplyAfter {
                return "", errDeadReply
            }
        }
        if w.deadlinePassed(start) {
            if last != "" {
                w.logf("web: %s reply timed out after %ds; returning partial text", c.ID, w.timeout)
                return last, nil
            }
            return "", fmt.Errorf("timeout: no reply text within %ds", w.timeout)
        }
        time.Sleep(300 * time.Millisecond)
    }
}

func (w *Web) pageNotice() string {
    raw, err := w.page.Evaluate(`() => Array.from(document.querySelectorAll('mat-snack-bar-container, simple-snack-bar, [role="alert"], [role="status"], .error-message, .mdc-snackbar__label'))
        .map(e => e.innerText.trim()).filter(t => t).join(" | ")`, nil)
    if err != nil {
        return ""
    }
    if t, ok := raw.(string); ok {
        return strings.TrimSpace(t)
    }
    return ""
}

func (w *Web) copyReply() (string, error) {
    if err := w.ctx.GrantPermissions([]string{"clipboard-read", "clipboard-write"}); err != nil {
        return "", fmt.Errorf("clipboard permission: %v", err)
    }
    w.page.Evaluate(`() => navigator.clipboard.writeText("")`, nil)
    btn := w.page.Locator(replySel).Last().Locator("copy-button button").First()
    if err := btn.Click(pw.LocatorClickOptions{Timeout: pw.Float(5000)}); err != nil {
        return "", fmt.Errorf("copy button: %v", err)
    }
    for i := 0; i < 20; i++ {
        time.Sleep(150 * time.Millisecond)
        raw, err := w.page.Evaluate(`() => navigator.clipboard.readText()`, nil)
        if err != nil {
            return "", fmt.Errorf("clipboard read: %v", err)
        }
        if t, ok := raw.(string); ok && strings.TrimSpace(t) != "" {
            return strings.TrimSpace(t), nil
        }
    }
    return "", fmt.Errorf("clipboard stayed empty after copy")
}

func (w *Web) markdownOf(html string) string {
    out, err := w.mdConv.ConvertString(html)
    if err != nil {
        return strings.TrimSpace(html)
    }
    return strings.TrimSpace(tripleNewline.ReplaceAllString(out, "\n\n"))
}
