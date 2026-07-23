package turnstile

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/grok-free-register/grok-reg/internal/clearance"
)

// PoolBridge keeps a long-lived turnstile_pool.py process with N CloakBrowser slots.
// Concurrent Solve() calls are multiplexed over JSON-lines stdin/stdout.
type PoolBridge struct {
	ScriptPath string
	Python     string
	Proxy      string
	Clear      *clearance.Manager
	Workers    int
	Timeout    time.Duration
	// Mode: offscreen (default) | headless
	Mode string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *bufio.Writer
	stdout  *bufio.Reader
	nextID  atomic.Int64
	waiters map[int64]chan poolResp
	ready   bool
	closed  bool
	startMu sync.Mutex
}

type poolResp struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Token string `json:"token"`
	Error string `json:"error"`
	Event string `json:"event"`
}

func NewPoolBridge(proxy string, cm *clearance.Manager, workers int) *PoolBridge {
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	return &PoolBridge{
		ScriptPath: findPoolScript(),
		Python:     findPython(),
		Proxy:      proxy,
		Clear:      cm,
		Workers:    workers,
		Timeout:    100 * time.Second,
		Mode:       "offscreen",
		waiters:    map[int64]chan poolResp{},
	}
}

func (p *PoolBridge) Name() string { return "browser-pool" }

func (p *PoolBridge) Available() bool {
	return p.ScriptPath != "" && p.Python != ""
}

func (p *PoolBridge) ensureStarted(ctx context.Context) error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("pool closed")
	}
	if p.ready && p.cmd != nil && p.cmd.Process != nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	return p.start(ctx)
}

func (p *PoolBridge) start(ctx context.Context) error {
	if p.ScriptPath == "" {
		return fmt.Errorf("turnstile_pool.py not found; set GROK_TURNSTILE_POOL_SCRIPT or install scripts/")
	}
	if p.Python == "" {
		return fmt.Errorf("python not found for turnstile pool")
	}
	_ = p.kill()
	args := []string{
		p.ScriptPath,
		"--workers", fmt.Sprintf("%d", p.Workers),
	}
	if p.Proxy != "" {
		args = append(args, "--proxy", p.Proxy)
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
	cmd := exec.Command(bin, binArgs...)
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	p.mu.Lock()
	p.cmd = cmd
	p.stdin = bufio.NewWriter(stdin)
	p.stdout = bufio.NewReader(stdout)
	p.ready = false
	p.mu.Unlock()

	// Wait for ready event
	readyCh := make(chan error, 1)
	go func() {
		for {
			line, err := p.stdout.ReadString('\n')
			if err != nil {
				readyCh <- fmt.Errorf("pool stdout: %w", err)
				return
			}
			var r poolResp
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &r) != nil {
				continue
			}
			if r.Event == "ready" && r.OK {
				p.mu.Lock()
				p.ready = true
				p.mu.Unlock()
				readyCh <- nil
				// continue reading forever
				go p.readLoop()
				return
			}
			if !r.OK && r.Error != "" {
				readyCh <- fmt.Errorf("pool: %s", r.Error)
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		_ = p.kill()
		return ctx.Err()
	case err := <-readyCh:
		return err
	case <-time.After(90 * time.Second):
		_ = p.kill()
		return fmt.Errorf("pool start timeout")
	}
}

func (p *PoolBridge) readLoop() {
	for {
		line, err := p.stdout.ReadString('\n')
		if err != nil {
			p.failAll(fmt.Errorf("pool died: %w", err))
			return
		}
		var r poolResp
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &r) != nil {
			continue
		}
		if r.Event != "" && r.ID == 0 {
			continue
		}
		p.mu.Lock()
		ch := p.waiters[r.ID]
		if ch != nil {
			delete(p.waiters, r.ID)
		}
		p.mu.Unlock()
		if ch != nil {
			select {
			case ch <- r:
			default:
			}
		}
	}
}

func (p *PoolBridge) failAll(err error) {
	p.mu.Lock()
	p.ready = false
	waiters := p.waiters
	p.waiters = map[int64]chan poolResp{}
	p.mu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- poolResp{OK: false, Error: err.Error()}:
		default:
		}
	}
}

func (p *PoolBridge) Solve(ctx context.Context, siteKey, pageURL string) (string, error) {
	if pageURL == "" {
		pageURL = "https://accounts.x.ai/sign-up"
	}
	if err := p.ensureStarted(ctx); err != nil {
		return "", err
	}
	to := p.Timeout
	if to <= 0 {
		to = 100 * time.Second
	}
	// Use shorter of ctx deadline and pool timeout
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem > 0 && rem < to {
			to = rem
		}
	}
	id := p.nextID.Add(1)
	req := map[string]any{
		"id":       id,
		"site_key": siteKey,
		"url":      pageURL,
		"timeout":  to.Seconds(),
	}
	if injectClearance() && p.Clear != nil {
		if ua := p.Clear.UserAgent(); ua != "" {
			req["ua"] = ua
		}
	}
	raw, _ := json.Marshal(req)
	ch := make(chan poolResp, 1)
	p.mu.Lock()
	if !p.ready || p.stdin == nil {
		p.mu.Unlock()
		return "", fmt.Errorf("pool not ready")
	}
	p.waiters[id] = ch
	_, err := p.stdin.Write(append(raw, '\n'))
	if err == nil {
		err = p.stdin.Flush()
	}
	p.mu.Unlock()
	if err != nil {
		p.mu.Lock()
		delete(p.waiters, id)
		p.mu.Unlock()
		return "", fmt.Errorf("pool write: %w", err)
	}

	select {
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.waiters, id)
		p.mu.Unlock()
		return "", ctx.Err()
	case r := <-ch:
		if !r.OK || len(r.Token) <= 10 {
			if r.Error == "" {
				r.Error = "empty token"
			}
			return "", fmt.Errorf("pool mint: %s", truncate(r.Error, 300))
		}
		return r.Token, nil
	case <-time.After(to + 15*time.Second):
		p.mu.Lock()
		delete(p.waiters, id)
		p.mu.Unlock()
		return "", fmt.Errorf("pool mint timeout")
	}
}

func (p *PoolBridge) Close() {
	p.mu.Lock()
	p.closed = true
	p.ready = false
	p.mu.Unlock()
	_ = p.kill()
}

func (p *PoolBridge) kill() error {
	p.mu.Lock()
	cmd := p.cmd
	stdin := p.stdin
	p.cmd = nil
	p.stdin = nil
	p.ready = false
	p.mu.Unlock()
	if stdin != nil {
		// best-effort shutdown
		_, _ = stdin.WriteString(`{"cmd":"shutdown"}` + "\n")
		_ = stdin.Flush()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return nil
}

func findPoolScript() string {
	if p := strings.TrimSpace(os.Getenv("GROK_TURNSTILE_POOL_SCRIPT")); p != "" {
		if fileExists(p) {
			return p
		}
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "scripts", "turnstile_pool.py"),
			filepath.Join(dir, "turnstile_pool.py"),
			filepath.Join(dir, "..", "scripts", "turnstile_pool.py"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "scripts", "turnstile_pool.py"),
		)
	}
	// Prefer sibling of mint script
	if mint := findMintScript(); mint != "" {
		candidates = append([]string{filepath.Join(filepath.Dir(mint), "turnstile_pool.py")}, candidates...)
	}
	candidates = append(candidates,
		"/opt/Grok-Register/scripts/turnstile_pool.py",
		"/opt/Grok-Reg/scripts/turnstile_pool.py",
		"/usr/local/share/grok-reg/turnstile_pool.py",
	)
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

// DetectedPoolScript for startup logs.
func DetectedPoolScript() string { return findPoolScript() }
