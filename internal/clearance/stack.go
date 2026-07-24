package clearance

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
	all := append([]string{"compose"}, args...)
	cmd := exec.Command("docker", all...)
	cmd.Dir = dir
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

// LocalClearanceProxyDown is true when REGISTER_PROXY points at local privoxy
// (typically 127.0.0.1:40080) but that port is not accepting connections.
func LocalClearanceProxyDown(registerProxy string, privoxyPort int) bool {
	if privoxyPort <= 0 {
		privoxyPort = 40080
	}
	p := strings.TrimSpace(strings.ToLower(registerProxy))
	if p == "" {
		return false
	}
	// Only care about local clearance chain ports
	local := strings.Contains(p, "127.0.0.1") || strings.Contains(p, "localhost")
	portMatch := strings.Contains(p, fmt.Sprintf(":%d", privoxyPort))
	if !local || !portMatch {
		return false
	}
	return !portOpen("127.0.0.1", privoxyPort)
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
			// Ports up ≠ WARP tunnel ready. Wait until HTTP via privoxy works.
			if proxyEgressOK(privoxyPort, 8*time.Second) {
				return fmt.Sprintf("清障栈已就绪 dir=%s（端口+WARP 出口 OK）", dir), nil
			}
			last = "端口已开，等待 WARP 出口（via :40080）…"
			time.Sleep(3 * time.Second)
			continue
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

// proxyEgressOK checks that Privoxy can fetch a trivial HTTPS URL (WARP path live).
func proxyEgressOK(privoxyPort int, timeout time.Duration) bool {
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", privoxyPort)
	u, err := url.Parse(proxyURL)
	if err != nil {
		return false
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(u),
		},
	}
	// cloudflare trace is light and works over WARP
	resp, err := client.Get("https://www.cloudflare.com/cdn-cgi/trace")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode > 0 && resp.StatusCode < 500
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
