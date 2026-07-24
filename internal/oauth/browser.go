package oauth

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DeviceMode controls how device consent is completed.
//
//	http    — pure HTTP form posts (legacy; new accounts often invalid_grant)
//	browser — always Playwright click Allow
//	auto    — HTTP first; on invalid_grant / incomplete, retry once with browser
const (
	DeviceModeHTTP    = "http"
	DeviceModeBrowser = "browser"
	DeviceModeAuto    = "auto"
)

// ConfirmBrowser runs scripts/oauth_device_approve.py to complete device consent.
func (c *Client) ConfirmBrowser(ctx context.Context, sso string, flow DeviceFlow) error {
	sso = strings.TrimSpace(sso)
	if sso == "" {
		return fmt.Errorf("login_required")
	}
	url := strings.TrimSpace(flow.VerificationURL)
	if url == "" {
		url = "https://accounts.x.ai/oauth2/device?user_code=" + flow.UserCode
	}
	py := findPython()
	script := findDeviceApproveScript()
	if py == "" || script == "" {
		return fmt.Errorf("browser_device: python/script not found (need scripts/oauth_device_approve.py)")
	}
	chrome := strings.TrimSpace(os.Getenv("CHROME_PATH"))
	mode := strings.TrimSpace(os.Getenv("TURNSTILE_MODE"))
	if mode == "" {
		mode = "offscreen"
	}
	proxy := strings.TrimSpace(c.ProxyURL)
	if proxy == "" {
		proxy = strings.TrimSpace(os.Getenv("REGISTER_PROXY"))
	}
	if proxy == "" {
		proxy = strings.TrimSpace(os.Getenv("HTTPS_PROXY"))
	}

	args := []string{
		script,
		"--url", url,
		"--sso", sso,
		"--timeout", "90",
		"--mode", mode,
	}
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	if chrome != "" {
		args = append(args, "--chrome", chrome)
	}
	if c.ua != "" {
		args = append(args, "--ua", c.ua)
	}

	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Inherit DISPLAY for Xvfb offscreen; force proxy for child
	env := os.Environ()
	if proxy != "" {
		env = append(env, "REGISTER_PROXY="+proxy, "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy)
	}
	cmd.Env = env
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	errS := strings.TrimSpace(stderr.String())
	if err != nil {
		if errS == "" {
			errS = err.Error()
		}
		if len(errS) > 400 {
			errS = errS[:400] + "…"
		}
		return fmt.Errorf("browser_device: %s", errS)
	}
	if !strings.Contains(strings.ToLower(out), "ok") {
		// some versions only print ok on success line
		if errS != "" && !strings.Contains(strings.ToLower(errS), "ok") {
			return fmt.Errorf("browser_device: unexpected output %q err=%s", out, errS)
		}
	}
	c.ClearRateLimit()
	return nil
}

func findPython() string {
	for _, c := range []string{
		os.Getenv("TURNSTILE_PYTHON"),
		"/opt/cloakbrowser-venv/bin/python",
		"/usr/bin/python3",
		"python3",
		"python",
	} {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if strings.Contains(c, "/") {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func findDeviceApproveScript() string {
	if v := strings.TrimSpace(os.Getenv("OAUTH_DEVICE_SCRIPT")); v != "" {
		if st, err := os.Stat(v); err == nil && !st.IsDir() {
			return v
		}
	}
	candidates := []string{
		"/usr/local/share/grok-reg/oauth_device_approve.py",
		"/opt/Grok-Register/scripts/oauth_device_approve.py",
	}
	// relative to executable / cwd
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "oauth_device_approve.py"),
			filepath.Join(dir, "..", "share", "grok-reg", "oauth_device_approve.py"),
			filepath.Join(dir, "..", "scripts", "oauth_device_approve.py"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "scripts", "oauth_device_approve.py"),
			filepath.Join(wd, "oauth_device_approve.py"),
		)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
