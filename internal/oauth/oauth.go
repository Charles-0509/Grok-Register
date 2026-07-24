package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// ErrRateLimited is returned when auth.x.ai redirects with error=rate_limited.
var ErrRateLimited = errors.New("rate_limited")

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
	AccessToken   string
	RefreshToken  string
	IDToken       string
	TokenType     string
	ExpiresIn     int
	ExpiresAt     string
	LastRefresh   string
	Subject       string
	TokenEndpoint string
	Email         string
}

type Client struct {
	http  *http.Client
	ua    string
	clear *clearance.Manager

	// rate limit gate
	mu         sync.Mutex
	trippedAt  time.Time
	nextProbe  time.Time
	cooldown   time.Duration
	baseCool   time.Duration
	trips      int
	probeToken int
	probeSeq   int

	// OIDC discovery cache (device + token endpoints)
	discMu   sync.Mutex
	deviceEP string
	tokenEP  string
	discAt   time.Time
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
		if resp.StatusCode == 429 {
			c.TripRateLimit()
			return DeviceFlow{}, fmt.Errorf("%w: device authorization status=429", ErrRateLimited)
		}
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
	c.discMu.Lock()
	if c.deviceEP != "" && c.tokenEP != "" && time.Since(c.discAt) < 30*time.Minute {
		d, t := c.deviceEP, c.tokenEP
		c.discMu.Unlock()
		return d, t, nil
	}
	c.discMu.Unlock()

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
	c.discMu.Lock()
	c.deviceEP, c.tokenEP, c.discAt = deviceEP, tokenEP, time.Now()
	c.discMu.Unlock()
	return deviceEP, tokenEP, nil
}

// principalFromSSO extracts user id from session SSO JWT for device approve form.
func principalFromSSO(sso string) string {
	for _, key := range []string{"sub", "user_id", "userId", "uid", "id", "principal_id", "principalId"} {
		if v := jwtClaim(sso, key); v != "" {
			return v
		}
	}
	// nested claims common on some x.ai session tokens
	parts := strings.Split(sso, ".")
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
	for _, nest := range []string{"user", "account", "identity", "profile"} {
		if sub, ok := m[nest].(map[string]any); ok {
			for _, key := range []string{"sub", "id", "user_id", "userId", "uid"} {
				if v, ok := sub[key].(string); ok && v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func isDeviceDone(loc string) bool {
	return strings.Contains(loc, "/oauth2/device/done") || strings.Contains(loc, "device/done")
}

func isRedirect(code int) bool {
	return code == 301 || code == 302 || code == 303 || code == 307 || code == 308
}

// ConfirmHTTP posts verify + approve with SSO cookie (no browser).
// Must not treat bare 2xx HTML as authorized — that causes token poll invalid_grant.
func (c *Client) ConfirmHTTP(ctx context.Context, sso string, flow DeviceFlow) error {
	sso = strings.TrimSpace(sso)
	if sso == "" {
		return fmt.Errorf("login_required")
	}
	cookie := "sso=" + sso
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
	loc := resp.Header.Get("Location")
	vbody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if err := locationError(loc); err != nil {
		if errors.Is(err, ErrRateLimited) {
			c.TripRateLimit()
		}
		return err
	}
	if resp.StatusCode == 403 {
		return fmt.Errorf("challenge")
	}
	if isDeviceDone(loc) {
		c.ClearRateLimit()
		return nil
	}
	// verify must redirect to consent (or done). Bare 200 without location = not confirmed.
	if !isRedirect(resp.StatusCode) && loc == "" {
		preview := strings.TrimSpace(string(vbody))
		if len(preview) > 120 {
			preview = preview[:120]
		}
		return fmt.Errorf("device_verify_failed status=%d body=%q", resp.StatusCode, preview)
	}
	// approve
	consentRef := loc
	if consentRef == "" {
		consentRef = "https://accounts.x.ai/oauth2/device/consent?user_code=" + url.QueryEscape(flow.UserCode)
	} else if strings.HasPrefix(consentRef, "/") {
		consentRef = "https://accounts.x.ai" + consentRef
	}
	principal := principalFromSSO(sso)
	// Prefer principal_id from consent HTML if JWT has no sub (common after platform changes).
	if htmlPID := c.fetchConsentPrincipal(ctx, consentRef, cookie); htmlPID != "" {
		principal = htmlPID
	}
	aform := url.Values{
		"user_code":      {flow.UserCode},
		"action":         {"allow"},
		"principal_type": {"User"},
		"principal_id":   {principal},
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
		if errors.Is(err, ErrRateLimited) {
			c.TripRateLimit()
		}
		return err
	}
	text := strings.ToLower(string(body))
	if strings.Contains(text, "invalid action") {
		return fmt.Errorf("consent_invalid_action")
	}
	if strings.Contains(text, "device authorized") || strings.Contains(string(body), "设备已授权") {
		c.ClearRateLimit()
		return nil
	}
	if isDeviceDone(aloc) {
		c.ClearRateLimit()
		return nil
	}
	// Non-error redirect after allow is usually success (token poll is truth).
	if isRedirect(resp2.StatusCode) && aloc != "" && locationError(aloc) == nil {
		c.ClearRateLimit()
		return nil
	}
	if resp2.StatusCode == 403 {
		return fmt.Errorf("challenge")
	}
	// Do NOT accept generic 2xx without authorized markers — was causing invalid_grant.
	preview := strings.TrimSpace(string(body))
	if len(preview) > 160 {
		preview = preview[:160]
	}
	return fmt.Errorf("unknown_page status=%d loc=%q body=%q", resp2.StatusCode, aloc, preview)
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
	if e == "rate_limited" {
		return ErrRateLimited
	}
	return fmt.Errorf("%s", e)
}

func (c *Client) setFormHeaders(req *http.Request, referer, cookie string) {
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", "https://accounts.x.ai")
	req.Header.Set("Referer", referer)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", cookie)
	if c.clear != nil {
		if h := c.clear.CookieHeader(); h != "" {
			req.Header.Set("Cookie", cookie+"; "+h)
		}
	}
}

// fetchConsentPrincipal GETs consent page and parses hidden principal_id if present.
func (c *Client) fetchConsentPrincipal(ctx context.Context, consentURL, cookie string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, consentURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Cookie", cookie)
	if c.clear != nil {
		if h := c.clear.CookieHeader(); h != "" {
			req.Header.Set("Cookie", cookie+"; "+h)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	_ = resp.Body.Close()
	html := string(body)
	// name="principal_id" value="..."
	for _, pat := range []string{
		`name="principal_id" value="`,
		`name='principal_id' value='`,
		`name="principal_id" value='`,
		`"principal_id":"`,
	} {
		if i := strings.Index(html, pat); i >= 0 {
			rest := html[i+len(pat):]
			end := strings.IndexAny(rest, `"'`)
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return ""
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
			return Credential{}, fmt.Errorf("oauth_denied")
		case "expired_token":
			return Credential{}, fmt.Errorf("oauth_expired")
		case "invalid_grant":
			// Device not actually authorized — often false-positive ConfirmHTTP success.
			if errDesc != "" {
				return Credential{}, fmt.Errorf("oauth_rejected: invalid_grant (%s)", errDesc)
			}
			return Credential{}, fmt.Errorf("oauth_rejected: invalid_grant")
		default:
			if errCode != "" {
				if errDesc != "" {
					return Credential{}, fmt.Errorf("oauth_rejected: %s (%s)", errCode, errDesc)
				}
				return Credential{}, fmt.Errorf("oauth_rejected: %s", errCode)
			}
			return Credential{}, fmt.Errorf("oauth_rejected status=%d body=%s", resp.StatusCode, truncateBody(body, 120))
		}
		select {
		case <-ctx.Done():
			return Credential{}, ctx.Err()
		case <-time.After(interval):
		}
	}
	return Credential{}, fmt.Errorf("oauth_expired")
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

func truncateBody(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Exchange is convenience: start flow + confirm HTTP + poll.
// On rate_limited / device 429 / invalid_grant, retry with a fresh device code.
func (c *Client) Exchange(ctx context.Context, sso string) (Credential, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.WaitRateLimit(ctx); err != nil {
			return Credential{}, err
		}
		flow, err := c.StartDeviceFlow(ctx)
		if err != nil {
			last = err
			if (errors.Is(err, ErrRateLimited) || strings.Contains(err.Error(), "status=429")) && attempt < 2 {
				continue
			}
			return Credential{}, err
		}
		if err := c.ConfirmHTTP(ctx, sso, flow); err != nil {
			last = err
			if errors.Is(err, ErrRateLimited) && attempt < 2 {
				continue
			}
			// challenge / unknown_page: one more full attempt with new device code
			if attempt < 2 && (strings.Contains(err.Error(), "challenge") ||
				strings.Contains(err.Error(), "unknown_page") ||
				strings.Contains(err.Error(), "device_verify")) {
				continue
			}
			return Credential{}, err
		}
		cred, err := c.PollToken(ctx, flow)
		if err != nil {
			last = err
			// invalid_grant: consent did not stick — new device flow
			if attempt < 2 && strings.Contains(err.Error(), "invalid_grant") {
				continue
			}
			return Credential{}, err
		}
		return cred, nil
	}
	if last == nil {
		last = fmt.Errorf("oauth_failed")
	}
	return Credential{}, last
}
