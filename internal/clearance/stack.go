package clearance

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Default Privoxy images: vimagick 无 arm 多架构清单，arm64 用 lusky3。
const (
	privoxyImageAMD64 = "vimagick/privoxy:latest"
	privoxyImageARM64 = "lusky3/privoxy:latest"
)

// DefaultPrivoxyImage returns the arch-appropriate Privoxy image.
// PRIVOXY_IMAGE env overrides when set.
func DefaultPrivoxyImage() string {
	if v := strings.TrimSpace(os.Getenv("PRIVOXY_IMAGE")); v != "" {
		return v
	}
	switch runtime.GOARCH {
	case "arm64", "arm":
		return privoxyImageARM64
	default:
		// amd64, 386, etc.
		return privoxyImageAMD64
	}
}

// ensurePrivoxyImageEnv sets PRIVOXY_IMAGE for compose (and writes clearance/.env
// so bare `docker compose up` on this host keeps the same image).
func ensurePrivoxyImageEnv(composeDir string) {
	img := DefaultPrivoxyImage()
	_ = os.Setenv("PRIVOXY_IMAGE", img)
	if composeDir == "" {
		return
	}
	envPath := filepath.Join(composeDir, ".env")
	body := ""
	if data, err := os.ReadFile(envPath); err == nil {
		body = string(data)
	}
	lines := strings.Split(body, "\n")
	found := false
	out := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "PRIVOXY_IMAGE=") {
			if !found {
				out = append(out, "PRIVOXY_IMAGE="+img)
				found = true
			}
			continue
		}
		out = append(out, line)
	}
	// drop trailing empty lines from split of empty file
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if !found {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out,
			"# auto: host arch privoxy image (override with PRIVOXY_IMAGE)",
			"PRIVOXY_IMAGE="+img,
		)
	}
	text := strings.Join(out, "\n") + "\n"
	_ = os.WriteFile(envPath, []byte(text), 0o644)
}

// Project containers from clearance/docker-compose.yml
var stackContainerNames = []string{
	"grok-clearance-warp",
	"grok-clearance-privoxy",
	"grok-clearance-flaresolverr",
}

// ResolveComposeDir finds the clearance compose directory.
// Order: explicit, GROK_CLEARANCE_DIR, common install paths, cwd.
func ResolveComposeDir(explicit string) string {
	try := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			return ""
		}
		if st, err := os.Stat(filepath.Join(p, "docker-compose.yml")); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(p)
			return abs
		}
		return ""
	}
	if d := try(explicit); d != "" {
		return d
	}
	if d := try(os.Getenv("GROK_CLEARANCE_DIR")); d != "" {
		return d
	}
	for _, p := range []string{
		"/opt/Grok-Register/clearance",
		"/opt/Grok-Reg/clearance",
	} {
		if d := try(p); d != "" {
			return d
		}
	}
	// relative to executable
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, rel := range []string{
			filepath.Join(dir, "clearance"),
			filepath.Join(dir, "..", "clearance"),
			filepath.Join(dir, "..", "..", "clearance"),
		} {
			if d := try(rel); d != "" {
				return d
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if d := try(filepath.Join(wd, "clearance")); d != "" {
			return d
		}
		if d := try(filepath.Join(wd, "Grok-Register", "clearance")); d != "" {
			return d
		}
	}
	// macOS default install layout
	if home, err := os.UserHomeDir(); err == nil {
		if d := try(filepath.Join(home, "Grok-Register", "clearance")); d != "" {
			return d
		}
	}
	return ""
}

func dockerAvailable() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("未找到 docker 命令")
	}
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Docker 未运行或无权访问: %w", err)
	}
	return nil
}

func composeCmd(dir string, args ...string) *exec.Cmd {
	ensurePrivoxyImageEnv(dir)
	all := append([]string{"compose"}, args...)
	cmd := exec.Command("docker", all...)
	cmd.Dir = dir
	// Ensure child sees PRIVOXY_IMAGE even if parent only Setenv'd above.
	cmd.Env = append(os.Environ(), "PRIVOXY_IMAGE="+DefaultPrivoxyImage())
	return cmd
}

func runCompose(dir string, args ...string) (string, error) {
	cmd := composeCmd(dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if err != nil {
		if out == "" {
			return "", err
		}
		return out, fmt.Errorf("%w: %s", err, truncate(out, 400))
	}
	return out, nil
}

// StackRunning reports whether all clearance containers appear running.
func StackRunning() bool {
	for _, name := range stackContainerNames {
		cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = nil
		if err := cmd.Run(); err != nil {
			return false
		}
		if strings.TrimSpace(stdout.String()) != "true" {
			return false
		}
	}
	return true
}

// EnsureStack starts clearance compose if needed and waits for host ports.
// composeDir may be empty (auto-discover). Returns log-friendly message.
func EnsureStack(composeDir string, privoxyPort, flaresolverrPort int) (string, error) {
	if err := dockerAvailable(); err != nil {
		return "", err
	}
	dir := ResolveComposeDir(composeDir)
	if dir == "" {
		return "", fmt.Errorf("找不到 clearance/docker-compose.yml（可设 GROK_CLEARANCE_DIR）")
	}
	if privoxyPort <= 0 {
		privoxyPort = 40080
	}
	if flaresolverrPort <= 0 {
		flaresolverrPort = 8191
	}

	if StackRunning() && portOpen("127.0.0.1", privoxyPort) {
		// Still poke FlareSolverr lightly; if down, re-up
		if httpOK(fmt.Sprintf("http://127.0.0.1:%d/", flaresolverrPort), 2*time.Second) {
			return fmt.Sprintf("清障栈已在运行 dir=%s", dir), nil
		}
	}

	out, err := runCompose(dir, "up", "-d")
	if err != nil {
		return out, fmt.Errorf("docker compose up 失败: %w", err)
	}

	deadline := time.Now().Add(120 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		if portOpen("127.0.0.1", privoxyPort) && httpOK(fmt.Sprintf("http://127.0.0.1:%d/", flaresolverrPort), 3*time.Second) {
			return fmt.Sprintf("清障栈已就绪 dir=%s privoxy=%s", dir, DefaultPrivoxyImage()), nil
		}
		last = "等待 privoxy/flaresolverr 端口..."
		time.Sleep(2 * time.Second)
	}
	if last == "" {
		last = "timeout"
	}
	// not fatal hard fail — prewarm may still work partially
	return fmt.Sprintf("compose up 已执行 dir=%s，但健康检查超时: %s", dir, last), nil
}

// StopStack stops clearance compose services (frees CPU/RAM; keeps volumes).
func StopStack(composeDir string) (string, error) {
	if err := dockerAvailable(); err != nil {
		return "", err
	}
	dir := ResolveComposeDir(composeDir)
	if dir == "" {
		// fallback: stop by container name
		var stopped []string
		for _, name := range stackContainerNames {
			cmd := exec.Command("docker", "stop", name)
			if err := cmd.Run(); err == nil {
				stopped = append(stopped, name)
			}
		}
		if len(stopped) == 0 {
			return "无清障容器可停止", nil
		}
		return "已停止: " + strings.Join(stopped, ", "), nil
	}
	out, err := runCompose(dir, "stop")
	if err != nil {
		return out, fmt.Errorf("docker compose stop 失败: %w", err)
	}
	return fmt.Sprintf("清障栈已停止 dir=%s", dir), nil
}

func portOpen(host string, port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 800*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func httpOK(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode > 0 && resp.StatusCode < 500
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
