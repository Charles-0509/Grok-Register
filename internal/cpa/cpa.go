package cpa

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grok-free-register/grok-reg/internal/oauth"
)

const (
	CliproxyBase = "https://cli-chat-proxy.grok.com/v1"
)

var CliproxyHeaders = map[string]string{
	"x-grok-client-version":    "0.2.93",
	"x-xai-token-auth":         "xai-grok-cli",
	"X-XAI-Token-Auth":         "xai-grok-cli",
	"x-authenticateresponse":   "authenticate-response",
	"x-grok-client-identifier": "grok-shell",
	"x-compaction-at":          "400000",
	"User-Agent":               "grok-shell/0.2.93 (linux; x86_64)",
}

// Document is CPA-ready JSON.
type Document struct {
	Type          string            `json:"type"`
	AccessToken   string            `json:"access_token"`
	RefreshToken  string            `json:"refresh_token"`
	IDToken       string            `json:"id_token,omitempty"`
	TokenType     string            `json:"token_type,omitempty"`
	ExpiresIn     int               `json:"expires_in"`
	Expired       string            `json:"expired"`
	LastRefresh   string            `json:"last_refresh"`
	Sub           string            `json:"sub,omitempty"`
	Email         string            `json:"email,omitempty"`
	BaseURL       string            `json:"base_url"`
	TokenEndpoint string            `json:"token_endpoint"`
	AuthKind      string            `json:"auth_kind"`
	Headers       map[string]string `json:"headers"`
}

func FromCredential(cred oauth.Credential, email string) Document {
	em := email
	if em == "" {
		em = cred.Email
	}
	return Document{
		Type:          "xai",
		AccessToken:   cred.AccessToken,
		RefreshToken:  cred.RefreshToken,
		IDToken:       cred.IDToken,
		TokenType:     cred.TokenType,
		ExpiresIn:     cred.ExpiresIn,
		Expired:       cred.ExpiresAt,
		LastRefresh:   cred.LastRefresh,
		Sub:           cred.Subject,
		Email:         em,
		BaseURL:       CliproxyBase,
		TokenEndpoint: cred.TokenEndpoint,
		AuthKind:      "oauth",
		Headers:       cloneHeaders(CliproxyHeaders),
	}
}

func Filename(doc Document, secret []byte) string {
	subject := doc.Sub
	if subject == "" {
		subject = doc.RefreshToken
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(subject))
	return "xai-" + hex.EncodeToString(mac.Sum(nil))[:16] + ".json"
}

func WriteAtomic(dir string, doc Document, secret []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := Filename(doc, secret)
	path := filepath.Join(dir, name)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

// Probe hits cli-chat-proxy with minimal responses call (acpa_watchdog shape).
// New tokens often get transient 403 permission-denied; 5xx/timeout also retry.
// warmupSec: sleep before first attempt (0 uses 1.5s default; negative = no sleep).
func Probe(doc Document, proxy string, warmupSec ...float64) error {
	_ = proxy
	warm := 1.5
	if len(warmupSec) > 0 {
		warm = warmupSec[0]
	}
	if warm > 0 {
		time.Sleep(time.Duration(warm * float64(time.Second)))
	}

	// attempt 0 immediate after warmup; then 3s, 6s, 12s, 20s (covers slow account provision)
	backs := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second, 20 * time.Second}
	var last error
	for attempt := 0; attempt < len(backs); attempt++ {
		if backs[attempt] > 0 {
			time.Sleep(backs[attempt])
		}
		err := probeOnce(doc)
		if err == nil {
			return nil
		}
		last = err
		if probeRetryable(err) {
			continue
		}
		return err
	}
	return last
}

func probeRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "permission-denied") ||
		strings.Contains(msg, "chat endpoint is denied") ||
		strings.Contains(msg, "http=403") ||
		strings.Contains(msg, "http=429") ||
		strings.Contains(msg, "http=500") ||
		strings.Contains(msg, "http=502") ||
		strings.Contains(msg, "http=503") ||
		strings.Contains(msg, "http=504") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") {
		return true
	}
	return false
}

func probeOnce(doc Document) error {
	client := &http.Client{Timeout: 45 * time.Second}
	// Match keys/acpa_watchdog.py body exactly — bare content string can 403.
	payload := map[string]any{
		"model":             "grok-4.5",
		"store":             false,
		"stream":            false,
		"max_output_tokens": 16,
		"input": []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "ok"},
				},
			},
		},
	}
	raw, _ := json.Marshal(payload)
	base := strings.TrimRight(doc.BaseURL, "/")
	if base == "" {
		base = CliproxyBase
	}
	url := base + "/responses"
	if strings.HasSuffix(base, "/responses") {
		url = base
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	sid := "probe-" + doc.Sub
	if doc.Sub == "" {
		sid = fmt.Sprintf("probe-%d", time.Now().UnixNano())
	}
	rid := fmt.Sprintf("%d", time.Now().UnixNano())
	req.Header.Set("Authorization", "Bearer "+doc.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range doc.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("x-grok-session-id", sid)
	req.Header.Set("x-grok-conv-id", sid)
	req.Header.Set("x-grok-req-id", rid)
	req.Header.Set("x-grok-turn-idx", "1")
	if len(rid) >= 8 {
		req.Header.Set("x-grok-agent-id", "agent-"+rid[:8])
	}
	req.Header.Set("x-grok-model-override", "grok-4.5")
	if doc.Email != "" {
		req.Header.Set("x-email", doc.Email)
	}
	if doc.Sub != "" {
		req.Header.Set("x-userid", doc.Sub)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	txt := string(body)
	low := strings.ToLower(txt)
	if resp.StatusCode == 200 {
		return nil
	}
	// free exhausted / rate limit: still treat as "alive enough" for CPA count?
	// Match watchdog: only 200 is alive; return error with marker.
	if resp.StatusCode == 429 || strings.Contains(low, "free-usage-exhausted") || strings.Contains(low, "rate limit") {
		return fmt.Errorf("probe http=%d rate/exhausted body=%s", resp.StatusCode, truncate(txt, 120))
	}
	return fmt.Errorf("probe http=%d body=%s", resp.StatusCode, truncate(txt, 160))
}

func AppendSSO(accountsPath, email, password, sso string) error {
	if err := os.MkdirAll(filepath.Dir(accountsPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(accountsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s:%s:%s\n", email, password, sso)
	return err
}

// AppendGrok2APIToken writes one SSO/session token per line (no email/password).
// For importers that only accept a bare token (e.g. grok2api).
func AppendGrok2APIToken(dir, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "tokens.txt")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", token)
	return err
}

func AppendAuthSession(path, email, sso string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	doc := map[string]any{
		"email": email,
		"cookies": []map[string]string{
			{"name": "sso", "value": sso, "domain": ".x.ai", "path": "/"},
		},
	}
	raw, _ := json.Marshal(doc)
	_, err = f.Write(append(raw, '\n'))
	return err
}

func cloneHeaders(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// DefaultSecret for filename hmac (local only).
func DefaultSecret() []byte {
	return []byte("grok-reg-local-cpa-name-secret")
}
