package turnstile

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// needsVirtualDisplay is true when mode launches a headed Chromium
// (offscreen/auto). True headless does not need X11.
func needsVirtualDisplay(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "headless" {
		return false
	}
	// offscreen | auto | "" → headed, needs DISPLAY
	return true
}

// hasDisplay reports whether a usable X11/Wayland display is configured.
func hasDisplay() bool {
	if d := strings.TrimSpace(os.Getenv("DISPLAY")); d != "" {
		return true
	}
	// Wayland-only sessions sometimes lack DISPLAY but still work for Chromium
	// with ozone; Playwright headed still expects X on Linux servers.
	if w := strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")); w != "" {
		return true
	}
	return false
}

var (
	xvfbPathOnce sync.Once
	xvfbPath     string
)

func findXvfbRun() string {
	xvfbPathOnce.Do(func() {
		if p, err := exec.LookPath("xvfb-run"); err == nil {
			xvfbPath = p
			return
		}
		for _, c := range []string{"/usr/bin/xvfb-run", "/bin/xvfb-run"} {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				xvfbPath = c
				return
			}
		}
	})
	return xvfbPath
}

// DisplayHint is a short status string for startup logs (DISPLAY / xvfb / headless).
func DisplayHint(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		m = "offscreen"
	}
	if !needsVirtualDisplay(m) {
		return "true-headless"
	}
	if d := strings.TrimSpace(os.Getenv("DISPLAY")); d != "" {
		return "DISPLAY=" + d
	}
	if w := strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")); w != "" {
		return "WAYLAND_DISPLAY=" + w
	}
	if findXvfbRun() != "" {
		return "xvfb-run (no $DISPLAY)"
	}
	return "no-display (install xvfb)"
}

// wrapForDisplay returns argv[0] and full args for launching a headed browser
// process when the host has no X server.
//
// On Linux with mode=offscreen and empty DISPLAY, wraps with:
//
//	xvfb-run -a -s "-screen 0 1280x720x24" <python> ...
//
// Returns an error if a virtual display is required but xvfb-run is missing.
func wrapForDisplay(mode, python string, pyArgs []string) (bin string, args []string, err error) {
	if !needsVirtualDisplay(mode) || runtime.GOOS != "linux" || hasDisplay() {
		return python, pyArgs, nil
	}
	xvfb := findXvfbRun()
	if xvfb == "" {
		return "", nil, fmt.Errorf(
			"TURNSTILE_MODE=%s 需要有头 Chromium，但本机无 $DISPLAY 且未找到 xvfb-run。\n"+
				"请任选其一：\n"+
				"  1) apt-get install -y xvfb   # 推荐，安装后重新 grok start（会自动用 xvfb-run）\n"+
				"  2) export DISPLAY=:0         # 若已有图形会话\n"+
				"  3) TURNSTILE_MODE=headless   # 真无头（易触发 CF 600010，不推荐）",
			strings.TrimSpace(mode),
		)
	}
	// -a: pick a free display number
	// -s: Xvfb screen geometry (enough for 800x600 offscreen window)
	wrapped := make([]string, 0, 6+len(pyArgs))
	wrapped = append(wrapped, "-a", "-s", "-screen 0 1280x720x24", python)
	wrapped = append(wrapped, pyArgs...)
	return xvfb, wrapped, nil
}
