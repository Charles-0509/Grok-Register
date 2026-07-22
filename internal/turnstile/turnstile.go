package turnstile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/grok-free-register/grok-reg/internal/clearance"
)

// Provider mints Cloudflare Turnstile tokens for accounts.x.ai sign-up.
type Provider interface {
	Solve(ctx context.Context, siteKey, pageURL string) (string, error)
	Name() string
}

// Optional closer for browser allocator.
type Closer interface {
	Close()
}

// Lite is YesCaptcha-shaped createTask/getTaskResult client (optional external farm).
type Lite struct {
	BaseURL string
	Client  *http.Client
}

func NewLite(baseURL string) *Lite {
	return &Lite{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (l *Lite) Name() string { return "lite" }

func (l *Lite) Solve(ctx context.Context, siteKey, pageURL string) (string, error) {
	createBody := map[string]any{
		"clientKey": "grok-reg",
		"task": map[string]any{
			"type":       "TurnstileTaskProxyless",
			"websiteURL": pageURL,
			"websiteKey": siteKey,
		},
	}
	raw, _ := json.Marshal(createBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/createTask", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.Client.Do(req)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	if created["taskId"] == nil {
		return "", fmt.Errorf("lite createTask failed: %s", string(body))
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
		gb, _ := json.Marshal(map[string]any{"clientKey": "grok-reg", "taskId": created["taskId"]})
		req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/getTaskResult", bytes.NewReader(gb))
		req2.Header.Set("Content-Type", "application/json")
		resp2, err := l.Client.Do(req2)
		if err != nil {
			continue
		}
		b2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
		_ = resp2.Body.Close()
		var res map[string]any
		_ = json.Unmarshal(b2, &res)
		status, _ := res["status"].(string)
		if status == "ready" || status == "completed" {
			if sol, ok := res["solution"].(map[string]any); ok {
				if tok, _ := sol["token"].(string); tok != "" {
					return tok, nil
				}
				if tok, _ := sol["gRecaptchaResponse"].(string); tok != "" {
					return tok, nil
				}
			}
		}
		if status == "failed" || status == "error" {
			return "", fmt.Errorf("lite solve failed: %s", string(b2))
		}
	}
	return "", fmt.Errorf("lite solve timeout")
}

// Options for New().
type Options struct {
	Provider string
	LiteURL  string
	Proxy    string
	Clear    *clearance.Manager
	// Workers: parallel persistent CloakBrowser slots (browser pool). 0 = auto (2).
	Workers int
}

// chain tries primary then fallback.
type chain struct {
	name string
	list []Provider
}

func (c *chain) Name() string { return c.name }

func (c *chain) Solve(ctx context.Context, siteKey, pageURL string) (string, error) {
	var last error
	var errs []string
	for _, p := range c.list {
		tok, err := p.Solve(ctx, siteKey, pageURL)
		if err == nil && len(tok) > 10 {
			return tok, nil
		}
		if err != nil {
			last = err
			errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err))
		} else {
			last = fmt.Errorf("%s: empty token", p.Name())
			errs = append(errs, last.Error())
		}
	}
	if last == nil {
		last = fmt.Errorf("no provider")
	}
	if len(errs) > 1 {
		return "", fmt.Errorf("%s", strings.Join(errs, " | "))
	}
	return "", last
}

func (c *chain) Close() {
	for _, p := range c.list {
		if cl, ok := p.(Closer); ok {
			cl.Close()
		}
	}
}

// New returns a provider. Default browser uses Playwright script (original project path).
func New(opts Options) Provider {
	name := strings.ToLower(strings.TrimSpace(opts.Provider))
	if name == "" {
		name = "browser"
	}
	switch name {
	case "lite", "farm", "yescaptcha", "local-solver", "turnstile-solver":
		url := opts.LiteURL
		if url == "" {
			url = "http://127.0.0.1:5072"
		}
		return NewLite(url)
	case "chromedp":
		return NewBrowser(opts.Proxy, opts.Clear)
	case "browser", "local", "playwright", "pool":
		// Prefer persistent pool (parallel CloakBrowser), then one-shot mint script, then chromedp.
		workers := opts.Workers
		if workers <= 0 {
			workers = 2
		}
		pool := NewPoolBridge(opts.Proxy, opts.Clear, workers)
		pw := NewPlaywrightBridge(opts.Proxy, opts.Clear)
		var list []Provider
		if pool.Available() {
			list = append(list, pool)
		}
		if pw.ScriptPath != "" && pw.Python != "" {
			list = append(list, pw)
		}
		list = append(list, NewBrowser(opts.Proxy, opts.Clear))
		if len(list) == 1 {
			return list[0]
		}
		return &chain{name: "browser", list: list}
	default:
		return NewPlaywrightBridge(opts.Proxy, opts.Clear)
	}
}
