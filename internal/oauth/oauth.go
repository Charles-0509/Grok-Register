package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/grok-free-register/grok-reg/internal/clearance"
)

const (
	DiscoveryURL = "https://auth.x.ai/.well-known/openid-configuration"
	ClientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	Scope        = "openid profile email offline_access grok-cli:access api:access"
	VerifyURL    = "https://auth.x.ai/oauth2/device/verify"
	ApproveURL   = "https://auth.x.ai/oauth2/device/approve"
	DefaultUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
)

type DeviceFlow struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	ExpiresIn       int
	Interval        float64
	TokenEndpoint   string
}

type Credential struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	ExpiresIn    int
	ExpiresAt    string
	LastRefresh  string
	Subject      string
	TokenEndpoint string
	Email        string
}

type Client struct {
	http  *http.Client
	ua    string
	clear *clearance.Manager

	// rate limit gate
	mu          sync.Mutex
	trippedAt   time.Time
	nextProbe   time.Time
	cooldown    time.Duration
	baseCool    time.Duration
	trips       int
	probeToken  int
	probeSeq    int
}

func NewClient(proxy string, cm *clearance.Manager, baseCooldown time.Duration) (*Client, error) {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	if baseCooldown <= 0 {
		baseCooldown = 60 * time.Second
	}
	c := &Client{
		http: &http.Client{
			Timeout:   45 * time.Second,
			Jar:       jar,
			Transport: tr,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ua:       DefaultUA,
		clear:    cm,
		baseCool: baseCooldown,
		cooldown: baseCooldown,
	}
	if cm != nil {
		c.ua = cm.UserAgent()
	}
	return c, nil
}

func (c *Client) WaitRateLimit(ctx context.Context) error {
	for {
		c.mu.Lock()
		if c.trippedAt.IsZero() {
			c.mu.Unlock()
			return nil
		}
		now := time.Now()
		if now.Before(c.nextProbe) {
			wait := time.Until(c.nextProbe)
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
				continue
			}
		}
		// allow one probe
		c.probeSeq++
		c.probeToken = c.probeSeq
		c.mu.Unlock()
		return nil
	}
}

func (c *Client) TripRateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.trippedAt.IsZero() {
		c.trippedAt = now
		c.trips = 1
	} else {
		c.trips++
	}
	// growth 1.5^n capped 300s
	cool := float64(c.baseCool) * pow15(c.trips-1)
	if cool > float64(300*time.Second) {
		cool = float64(300 * time.Second)
	}
	c.cooldown = time.Duration(cool)
	c.nextProbe = now.Add(c.cooldown)
}

func (c *Client) ClearRateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trippedAt = time.Time{}
	c.nextProbe = time.Time{}
	c.trips = 0
	c.cooldown = c.baseCool
}

func pow15(n int) float64 {
	v := 1.0
	for i := 0; i < n; i++ {
		v *= 1.5
	}
	return v
}

func (c *Client) StartDeviceFlow(ctx context.Context) (DeviceFlow, error) {
	devEP, tokEP, err := c.discover(ctx)
	if err != nil {
		return DeviceFlow{}, err
	}
	form := url.Values{}
	form.Set("client_id", ClientID)
	form.Set("scope", Scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, devEP, strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceFlow{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.http.Do(req)
	if err != nil {
		return DeviceFlow{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return DeviceFlow{}, fmt.Errorf("device authorization rejected status=%d", resp.StatusCode)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return DeviceFlow{}, err
	}
	dc, _ := doc["device_code"].(string)
	uc, _ := doc["user_code"].(string)
	baseURL, _ := doc["verification_uri"].(string)
	if baseURL == "" {
		baseURL, _ = doc["verification_url"].(string)
	}
	exp, _ := doc["expires_in"].(float64)
	interval, _ := doc["interval"].(float64)
	if interval <= 0 {
		interval = 5
	}
	vurl, _ := doc["verification_uri_complete"].(string)
	if vurl == "" {
		sep := "?"
		if strings.Contains(baseURL, "?") {
			sep = "&"
		}
		vurl = baseURL + sep + "user_code=" + url.QueryEscape(uc)
	}
	return DeviceFlow{
		DeviceCode:      dc,
		UserCode:        uc,
		VerificationURL: vurl,
		ExpiresIn:       int(exp),
		Interval:        interval,
		TokenEndpoint:   tokEP,
	}, nil
}

func (c *Client) discover(ctx context.Context) (deviceEP, tokenEP string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DiscoveryURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("discovery rejected")
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", "", err
	}
	deviceEP, _ = doc["device_authorization_endpoint"].(string)
	tokenEP, _ = doc["token_endpoint"].(string)
	if deviceEP == "" || tokenEP == "" {
		return "", "", fmt.Errorf("discovery missing endpoints")
	}
	return deviceEP, tokenEP, nil
}

// seedSSO puts the session SSO into the jar for auth/accounts hosts and
// returns the Cookie header value used for device confirm.
func (c *Client) seedSSO(sso string) string {
	sso = strings.TrimSpace(sso)
	if sso == "" {
		return ""
	}
	ck := &http.Cookie{Name: "sso", Value: sso, Path: "/", Secure: true, HttpOnly: true}
	if c.http.Jar != nil {
		for _, raw := range []string{
			"https://auth.x.ai/",
			"https://accounts.x.ai/",
			"https://x.ai/",
		} {
			if u, err := url.Parse(raw); err == nil {
				c.http.Jar.SetCookies(u, []*http.Cookie{ck})
			}
		}
	}
	return "sso=" + sso
}

// warmDevicePage GETs the verification URL so the server associates the SSO
// session with the user_code before verify/approve POSTs.
func (c *Client) warmDevicePage(ctx context.Context, cookie string, flow DeviceFlow) {
	vurl := strings.TrimSpace(flow.VerificationURL)
	if vurl == "" {
		vurl = "https://accounts.x.ai/sign-in/device?user_code=" + url.QueryEscape(flow.UserCode)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vurl, nil)
	if err != nil {
		return
	}
	c.setNavHeaders(req, "https://accounts.x.ai/", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// ConfirmHTTP posts verify + approve with SSO cookie (no browser).
func (c *Client) ConfirmHTTP(ctx context.Context, sso string, flow DeviceFlow) error {
	if strings.TrimSpace(sso) == "" {
		return fmt.Errorf("oauth_sso_empty")
	}
	cookie := c.seedSSO(sso)
	c.warmDevicePage(ctx, cookie, flow)

	// verify
	form := url.Values{"user_code": {flow.UserCode}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, VerifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	c.setFormHeaders(req, flow.VerificationURL, cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	vbody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	if err := locationError(loc); err != nil {
		if err.Error() == "rate_limited" {
			c.TripRateLimit()
		}
		return fmt.Errorf("oauth_verify: %w", err)
	}
	if resp.StatusCode == 403 {
		return fmt.Errorf("oauth_verify: challenge")
	}
	if looksLikeLogin(string(vbody), loc) {
		return fmt.Errorf("oauth_verify: sso_rejected (login redirect) status=%d", resp.StatusCode)
	}
	if deviceAuthorized(string(vbody), loc) {
		c.ClearRateLimit()
		return nil
	}

	// approve
	consentRef := loc
	if consentRef == "" {
		consentRef = "https://accounts.x.ai/oauth2/device/consent?user_code=" + url.QueryEscape(flow.UserCode)
	} else if strings.HasPrefix(consentRef, "/") {
		// Prefer accounts host for relative consent paths from verify.
		if strings.Contains(consentRef, "auth.x.ai") {
			consentRef = "https://auth.x.ai" + consentRef
		} else {
			consentRef = "https://accounts.x.ai" + consentRef
		}
	}
	aform := url.Values{
		"user_code":      {flow.UserCode},
		"action":         {"allow"},
		"principal_type": {"User"},
		"principal_id":   {""},
	}
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, ApproveURL, strings.NewReader(aform.Encode()))
	if err != nil {
		return err
	}
	c.setFormHeaders(req2, consentRef, cookie)
	resp2, err := c.http.Do(req2)
	if err != nil {
		return err
	}
	aloc := resp2.Header.Get("Location")
	body, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	_ = resp2.Body.Close()
	if err := locationError(aloc); err != nil {
		if err.Error() == "rate_limited" {
			c.TripRateLimit()
		}
		return fmt.Errorf("oauth_approve: %w", err)
	}
	text := string(body)
	if deviceAuthorized(text, aloc) {
		c.ClearRateLimit()
		return nil
	}
	if resp2.StatusCode == 403 {
		return fmt.Errorf("oauth_approve: challenge")
	}
	if looksLikeLogin(text, aloc) {
		return fmt.Errorf("oauth_approve: sso_rejected (not logged in) status=%d", resp2.StatusCode)
	}
	// Do NOT treat bare 2xx / any redirect as success — that caused false
	// positives and later invalid_grant "device not authorized" on token poll.
	return fmt.Errorf("oauth_approve: not_authorized status=%d loc=%s body=%q",
		resp2.StatusCode, truncateStr(aloc, 120), truncateStr(text, 160))
}

func deviceAuthorized(body, loc string) bool {
	low := strings.ToLower(body)
	if strings.Contains(low, "device authorized") || strings.Contains(body, "设备已授权") {
		return true
	}
	if strings.Contains(loc, "/oauth2/device/done") || strings.Contains(loc, "device/done") {
		return true
	}
	// success-ish query flags seen on some builds
	if strings.Contains(loc, "authorized=true") || strings.Contains(loc, "status=authorized") {
		return true
	}
	return false
}

func looksLikeLogin(body, loc string) bool {
	lowLoc := strings.ToLower(loc)
	// Device verify/consent pages are expected; not a session failure.
	if strings.Contains(lowLoc, "sign-in/device") ||
		strings.Contains(lowLoc, "/oauth2/device/") ||
		strings.Contains(lowLoc, "device/consent") ||
		strings.Contains(lowLoc, "device/verify") ||
		strings.Contains(lowLoc, "device/approve") ||
		strings.Contains(lowLoc, "device/done") {
		return false
	}
	low := strings.ToLower(body + " " + loc)
	// Hard unauthenticated redirects / pages.
	for _, p := range []string{
		"/sign-in?", "/sign-in\"", "accounts.x.ai/sign-in",
		"auth.x.ai/u/login", "/u/login", `"sign in"`,
		"name=\"password\"", "type=\"password\"",
	} {
		if strings.Contains(low, p) {
			return true
		}
	}
	// Bare /sign-in or /login location without device path.
	if strings.Contains(lowLoc, "/sign-in") || strings.Contains(lowLoc, "/login") {
		return true
	}
	return false
}

func locationError(loc string) error {
	if loc == "" {
		return nil
	}
	u, err := url.Parse(loc)
	if err != nil {
		return nil
	}
	e := u.Query().Get("error")
	if e == "" {
		return nil
	}
	desc := u.Query().Get("error_description")
	if desc != "" {
		return fmt.Errorf("%s (%s)", e, desc)
	}
	return fmt.Errorf("%s", e)
}

func (c *Client) setFormHeaders(req *http.Request, referer, cookie string) {
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Device verify/approve live on auth.x.ai; prefer matching Origin when posting there.
	origin := "https://accounts.x.ai"
	if req.URL != nil && strings.Contains(req.URL.Host, "auth.x.ai") {
		origin = "https://auth.x.ai"
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", referer)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", cookie)
	if c.clear != nil {
		if h := c.clear.CookieHeader(); h != "" {
			req.Header.Set("Cookie", cookie+"; "+h)
		}
	}
}

func (c *Client) setNavHeaders(req *http.Request, referer, cookie string) {
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", referer)
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
		if c.clear != nil {
			if h := c.clear.CookieHeader(); h != "" {
				req.Header.Set("Cookie", cookie+"; "+h)
			}
		}
	}
}

func (c *Client) PollToken(ctx context.Context, flow DeviceFlow) (Credential, error) {
	deadline := time.Now().Add(time.Duration(flow.ExpiresIn) * time.Second)
	if flow.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	interval := time.Duration(flow.Interval * float64(time.Second))
	if interval < time.Second {
		interval = 5 * time.Second
	}
	for time.Now().Before(deadline) {
		form := url.Values{}
		form.Set("client_id", ClientID)
		form.Set("device_code", flow.DeviceCode)
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, flow.TokenEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return Credential{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", c.ua)
		resp, err := c.http.Do(req)
		if err != nil {
			return Credential{}, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		var doc map[string]any
		_ = json.Unmarshal(body, &doc)
		if resp.StatusCode/100 == 2 {
			return credentialFrom(doc, flow.TokenEndpoint)
		}
		errCode, _ := doc["error"].(string)
		errDesc, _ := doc["error_description"].(string)
		switch errCode {
		case "authorization_pending":
			// continue
		case "slow_down":
			interval += time.Second
		case "access_denied":
			return Credential{}, fmt.Errorf("oauth_denied: %s", firstNonEmpty(errDesc, "access_denied"))
		case "expired_token":
			return Credential{}, fmt.Errorf("oauth_expired")
		case "invalid_grant":
			// Typical when ConfirmHTTP false-succeeded or SSO was rejected silently.
			return Credential{}, fmt.Errorf("oauth_rejected: invalid_grant (%s) — device not authorized on auth.x.ai",
				firstNonEmpty(errDesc, "Access denied"))
		default:
			if errCode != "" {
				if errDesc != "" {
					return Credential{}, fmt.Errorf("oauth_rejected: %s (%s)", errCode, errDesc)
				}
				return Credential{}, fmt.Errorf("oauth_rejected: %s", errCode)
			}
			return Credential{}, fmt.Errorf("oauth_rejected status=%d body=%q", resp.StatusCode, truncateStr(string(body), 160))
		}
		select {
		case <-ctx.Done():
			return Credential{}, ctx.Err()
		case <-time.After(interval):
		}
	}
	return Credential{}, fmt.Errorf("oauth_expired")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func credentialFrom(doc map[string]any, endpoint string) (Credential, error) {
	at, _ := doc["access_token"].(string)
	rt, _ := doc["refresh_token"].(string)
	if at == "" || rt == "" {
		return Credential{}, fmt.Errorf("oauth_rejected: missing tokens")
	}
	id, _ := doc["id_token"].(string)
	tt, _ := doc["token_type"].(string)
	expF, _ := doc["expires_in"].(float64)
	exp := int(expF)
	if exp <= 0 {
		exp = 3600
	}
	now := time.Now().UTC()
	sub := jwtClaim(id, "sub")
	if sub == "" {
		sub = jwtClaim(at, "sub")
	}
	email := jwtClaim(id, "email")
	if email == "" {
		email = jwtClaim(at, "email")
	}
	return Credential{
		AccessToken:   at,
		RefreshToken:  rt,
		IDToken:       id,
		TokenType:     tt,
		ExpiresIn:     exp,
		ExpiresAt:     now.Add(time.Duration(exp) * time.Second).Format(time.RFC3339),
		LastRefresh:   now.Format(time.RFC3339),
		Subject:       sub,
		TokenEndpoint: endpoint,
		Email:         email,
	}, nil
}

func jwtClaim(token, key string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// Exchange is convenience: start flow + confirm HTTP + poll.
func (c *Client) Exchange(ctx context.Context, sso string) (Credential, error) {
	start := time.Now()
	if err := c.WaitRateLimit(ctx); err != nil {
		return Credential{}, err
	}
	flow, err := c.StartDeviceFlow(ctx)
	if err != nil {
		return Credential{}, fmt.Errorf("%w (%.1fs) sso=%s", err, time.Since(start).Seconds(), shortSSO(sso))
	}
	if err := c.ConfirmHTTP(ctx, sso, flow); err != nil {
		return Credential{}, fmt.Errorf("%w (%.1fs) sso=%s", err, time.Since(start).Seconds(), shortSSO(sso))
	}
	cred, err := c.PollToken(ctx, flow)
	if err != nil {
		return Credential{}, fmt.Errorf("%w (%.1fs) sso=%s", err, time.Since(start).Seconds(), shortSSO(sso))
	}
	return cred, nil
}

func shortSSO(sso string) string {
	sso = strings.TrimSpace(sso)
	if len(sso) <= 24 {
		return sso
	}
	return sso[:12] + "…" + sso[len(sso)-8:]
}
