package turnstile

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/grok-free-register/grok-reg/internal/clearance"
)

// PlaywrightBridge shells out to scripts/turnstile_mint.py (Playwright+CloakBrowser),
// matching the original grok_register mint path. chromedp alone is often blocked by CF.
type PlaywrightBridge struct {
	ScriptPath string
	Python     string
	Proxy      string
	Clear      *clearance.Manager
	Timeout    time.Duration
	// Mode: offscreen (default) | headless
	Mode string

	// serialize mint — original holds Physical_Sem for one solve
	mu sync.Mutex
}

func NewPlaywrightBridge(proxy string, cm *clearance.Manager) *PlaywrightBridge {
	return &PlaywrightBridge{
		ScriptPath: findMintScript(),
		Python:     findPython(),
		Proxy:      proxy,
		Clear:      cm,
		Timeout:    100 * time.Second,
		Mode:       "offscreen",
	}
}

func (p *PlaywrightBridge) Name() string { return "browser" }

func (p *PlaywrightBridge) Close() {}

func (p *PlaywrightBridge) Solve(ctx context.Context, siteKey, pageURL string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ScriptPath == "" {
		return "", fmt.Errorf("turnstile_mint.py not found; keep Grok-Reg/scripts next to binary or set GROK_TURNSTILE_SCRIPT")
	}
	if p.Python == "" {
		return "", fmt.Errorf("python3 not found for Playwright turnstile mint")
	}
	if pageURL == "" {
		pageURL = "https://accounts.x.ai/sign-up"
	}
	to := p.Timeout
	if to <= 0 {
		to = 100 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	args := []string{
		p.ScriptPath,
		"--site-key", siteKey,
		"--url", pageURL,
		"--timeout", fmt.Sprintf("%.0f", to.Seconds()),
	}
	if p.Proxy != "" {
		args = append(args, "--proxy", p.Proxy)
	}
	// Do NOT inject FlareSolverr cookies/UA by default.
	// Manual mint without them succeeds; injecting FS UA+cookies into CloakBrowser
	// often yields iframes=0 / no token (fingerprint + session mismatch).
	// Opt-in: GROK_TURNSTILE_INJECT_CLEARANCE=1
	if injectClearance() && p.Clear != nil {
		if h := p.Clear.CookieHeader(); h != "" {
			args = append(args, "--cookie", h)
		}
		if ua := p.Clear.UserAgent(); ua != "" {
			args = append(args, "--ua", ua)
		}
	}
	if chrome := strings.TrimSpace(os.Getenv("CHROME_PATH")); chrome != "" {
		args = append(args, "--chrome", chrome)
	}
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" || mode == "auto" {
		mode = "offscreen"
	}
	args = append(args, "--mode", mode)

	bin, binArgs := maybeXvfb(p.Python, args, mode)
	cmd := exec.CommandContext(ctx, bin, binArgs...)
	// inherit solver knobs
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	errText := strings.TrimSpace(stderr.String())
	if err != nil {
		if errText == "" {
			errText = err.Error()
		}
		return "", fmt.Errorf("playwright mint: %s", truncate(errText, 300))
	}
	if len(out) <= 10 {
		return "", fmt.Errorf("playwright mint: empty token %s", truncate(errText, 200))
	}
	// token must be single line
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = strings.TrimSpace(out[:i])
	}
	return out, nil
}

// DetectedPython / DetectedScript expose resolved mint paths for startup logs.
func DetectedPython() string { return findPython() }
func DetectedScript() string { return findMintScript() }

// maybeXvfb wraps python with xvfb-run when offscreen is requested and no DISPLAY.
// Lets headed Chromium run under a virtual framebuffer on headless servers.
func maybeXvfb(python string, args []string, mode string) (string, []string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "auto" {
		mode = "offscreen"
	}
	if mode == "headless" {
		return python, args
	}
	if strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != "" {
		return python, args
	}
	// explicit opt-out
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GROK_TURNSTILE_NO_XVFB")))
	if v == "1" || v == "true" || v == "yes" {
		return python, args
	}
	xvfb, err := exec.LookPath("xvfb-run")
	if err != nil {
		return python, args
	}
	// xvfb-run -a python script.py ...
	out := []string{"-a", python}
	out = append(out, args...)
	return xvfb, out
}

func injectClearance() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("GROK_TURNSTILE_INJECT_CLEARANCE")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func findPython() string {
	for _, name := range []string{
		os.Getenv("GROK_PYTHON"),
		"/opt/cloakbrowser-venv/bin/python",
		"/opt/Grok-Reg/.venv/bin/python",
		"python3",
		"python",
	} {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
		if strings.Contains(name, "/") {
			if st, err := os.Stat(name); err == nil && !st.IsDir() {
				return name
			}
		}
	}
	return ""
}

func findMintScript() string {
	if p := strings.TrimSpace(os.Getenv("GROK_TURNSTILE_SCRIPT")); p != "" {
		if fileExists(p) {
			return p
		}
	}
	var candidates []string
	// next to executable
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "scripts", "turnstile_mint.py"),
			filepath.Join(dir, "turnstile_mint.py"),
			filepath.Join(dir, "..", "scripts", "turnstile_mint.py"),
			filepath.Join(dir, "..", "Grok-Reg", "scripts", "turnstile_mint.py"),
		)
	}
	// cwd
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "scripts", "turnstile_mint.py"),
			filepath.Join(wd, "Grok-Reg", "scripts", "turnstile_mint.py"),
		)
	}
	// common install locations
	candidates = append(candidates,
		"/opt/Grok-Register/scripts/turnstile_mint.py",
		"/opt/Grok-Reg/scripts/turnstile_mint.py",
		"/usr/local/share/grok-reg/turnstile_mint.py",
	)
	// source relative to this file at build time — not available; skip
	_ = runtime.GOOS
	for _, c := range candidates {
		if fileExists(c) {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
