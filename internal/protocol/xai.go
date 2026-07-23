package protocol

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"github.com/grok-free-register/grok-reg/internal/clearance"
)

const (
	SiteURL       = "https://accounts.x.ai"
	ConnectCreate = SiteURL + "/auth_mgmt.AuthManagement/CreateEmailValidationCode"
	ConnectVerify = SiteURL + "/auth_mgmt.AuthManagement/VerifyEmailValidationCode"
	ConnectPass   = SiteURL + "/auth_mgmt.AuthManagement/ValidatePassword"
	SignupURLGrok = SiteURL + "/sign-up?redirect=grok-com"
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	// Document-aligned Next.js router state tree (URL-encoded at use).
	DefaultRouterStateTreeJSON = `["",{"children":["(app)",{"children":["(auth)",{"children":["sign-up",{"children":["__PAGE__",{},null,null,0]},null,null,0]},null,null,0]},null,null,0]},null,null,16]`
	// Fallback next-action id (scraped at runtime when possible).
	DefaultNextAction = "7f7f6cee188bd9cc17a3fb9dbde4abe224f21af0e3"
)

var (
	siteKeyRe = regexp.MustCompile(`0x4AAAAAAA[a-zA-Z0-9_-]+`)
	jsSrcRe   = regexp.MustCompile(`src="(/_next/static/[^"]+\.js)"`)
	hex40Re   = regexp.MustCompile(`[a-fA-F0-9]{40,50}`)
	flightRe  = regexp.MustCompile(`self\.__next_f\.push\(\[1,"(.*?)"\]\)`)
)

// ClientOptions configures protocol HTTP client.
type ClientOptions struct {
	Proxy            string
	Clearance        *clearance.Manager
	Impersonate      string   // e.g. chrome_131
	ImpersonateFallback []string
	Timeout          time.Duration
}

type SignupConfig struct {
	SiteKey   string
	ActionID  string
	StateTree string
	Source    string
}

type Client struct {
	sess    *Session
	proxy   string
	clear   *clearance.Manager
	ua      string
	profile string
	mu      sync.Mutex
	cfg     SignupConfig
}

func NewClient(proxy string, cm *clearance.Manager) (*Client, error) {
	return NewClientOpts(ClientOptions{Proxy: proxy, Clearance: cm, Impersonate: "chrome_131"})
}

func NewClientOpts(opt ClientOptions) (*Client, error) {
	profile := opt.Impersonate
	if profile == "" {
		profile = "chrome_131"
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	sess, err := NewSession(opt.Proxy, profile, timeout)
	if err != nil {
		return nil, err
	}
	c := &Client{
		sess:    sess,
		proxy:   opt.Proxy,
		clear:   opt.Clearance,
		ua:      DefaultUserAgent,
		profile: sess.ProfileName(),
	}
	if opt.Clearance != nil {
		if ua := opt.Clearance.UserAgent(); ua != "" {
			c.ua = ua
			sess.SetUserAgent(ua)
		}
		c.applyClearanceCookies()
	}
	_ = opt.ImpersonateFallback
	return c, nil
}

// Profile returns active TLS impersonation name.
func (c *Client) Profile() string {
	if c == nil {
		return ""
	}
	return c.profile
}

// RecreateWithProfile rebuilds the TLS session with a different impersonate profile.
func (c *Client) RecreateWithProfile(profile string) error {
	if c == nil {
		return fmt.Errorf("nil client")
	}
	sess, err := NewSession(c.proxy, profile, 45*time.Second)
	if err != nil {
		return err
	}
	c.sess = sess
	c.profile = sess.ProfileName()
	sess.SetUserAgent(c.ua)
	c.applyClearanceCookies()
	return nil
}

func (c *Client) applyClearanceCookies() {
	if c.clear == nil || c.sess == nil {
		return
	}
	b := c.clear.Get()
	var cookies []*http.Cookie
	for _, ck := range b.Cookies {
		cookies = append(cookies, &http.Cookie{
			Name:   ck.Name,
			Value:  ck.Value,
			Domain: ck.Domain,
			Path:   ck.Path,
		})
	}
	if len(cookies) > 0 {
		c.sess.SetCookies(SiteURL, cookies)
	}
	if b.UserAgent != "" {
		c.ua = b.UserAgent
		c.sess.SetUserAgent(b.UserAgent)
	}
}

func (c *Client) Config() SignupConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// WarmSignup GETs sign-up page; used for CF probe and config scrape.
func (c *Client) WarmSignup() (status int, body string, err error) {
	c.applyClearanceCookies()
	req, err := NewRequest(http.MethodGet, SignupURLGrok, nil)
	if err != nil {
		return 0, "", err
	}
	c.setBrowserHeaders(req)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Referer", "https://grok.com/")
	resp, err := c.sess.Do(req)
	if err != nil {
		return 0, "", Wrap(CodeWarm, "GET sign-up", err)
	}
	html, err := readBody(resp)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, html, nil
}

func (c *Client) FetchConfig() (SignupConfig, error) {
	status, html, err := c.WarmSignup()
	if err != nil {
		return SignupConfig{}, err
	}
	cfg := SignupConfig{Source: fmt.Sprintf("http status=%d profile=%s", status, c.profile)}
	if status != 200 || isCloudflare(status, html, nil) {
		cfg.Source += " (blocked_or_empty)"
		code := CodeCFBlocked
		if status == 403 {
			code = CodeCF403
		}
		return cfg, Failf(code, "signup page blocked status=%d profile=%s", status, c.profile)
	}
	if m := siteKeyRe.FindString(html); m != "" {
		cfg.SiteKey = m
	}
	cfg.StateTree = scrapeStateTree(html)
	if cfg.StateTree == "" {
		cfg.StateTree = url.QueryEscape(DefaultRouterStateTreeJSON)
		cfg.Source += "+default_tree"
	}
	jsURLs := unique(jsSrcRe.FindAllStringSubmatch(html, -1))
	for _, path := range jsURLs {
		if cfg.ActionID != "" {
			break
		}
		js, err := c.fetchJS(path)
		if err != nil || js == "" {
			continue
		}
		if !strings.Contains(js, "createUser") && !strings.Contains(js, "registerUser") && !strings.Contains(js, "emailValidation") {
			continue
		}
		if hexes := hex40Re.FindAllString(js, -1); len(hexes) > 0 {
			cfg.ActionID = hexes[0]
			cfg.Source += "+scrape_action"
		}
	}
	if cfg.ActionID == "" {
		cfg.ActionID = DefaultNextAction
		cfg.Source += "+default_action"
	}
	if cfg.SiteKey == "" {
		cfg.SiteKey = "0x4AAAAAAAhr9JGVDZbrZOo0"
		cfg.Source += "+default_sitekey"
	}
	if cfg.SiteKey == "" || cfg.ActionID == "" || cfg.StateTree == "" {
		return cfg, Failf(CodeConfig, "config incomplete site_key=%v action=%v state=%v", cfg.SiteKey != "", cfg.ActionID != "", cfg.StateTree != "")
	}
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	return cfg, nil
}

func (c *Client) fetchJS(path string) (string, error) {
	req, err := NewRequest(http.MethodGet, SiteURL+path, nil)
	if err != nil {
		return "", err
	}
	c.setBrowserHeaders(req)
	req.Header.Set("Referer", SignupURLGrok)
	resp, err := c.sess.Do(req)
	if err != nil {
		return "", err
	}
	return readBody(resp)
}

// CreateEmailCode sends gRPC-Web CreateEmailValidationCode.
// castleToken may be empty (current strategy).
func (c *Client) CreateEmailCode(email string) error {
	return c.CreateEmailCodeCastle(email, "")
}

func (c *Client) CreateEmailCodeCastle(email, castleToken string) error {
	inner := pbStr(1, email)
	if castleToken != "" {
		inner = append(inner, pbStr(3, castleToken)...)
	}
	frame := grpcWebFrame(inner)
	req, err := NewRequest(http.MethodPost, ConnectCreate, bytes.NewReader(frame))
	if err != nil {
		return err
	}
	c.setGRPCHeaders(req)
	resp, err := c.sess.Do(req)
	if err != nil {
		return Wrap(CodeGRPCCreate, "create email", err)
	}
	body, _ := readAllBody(resp)
	st := readGRPCStatus(resp, body)
	if st == "" {
		st = "0"
	}
	if resp.StatusCode != 200 || (st != "0" && st != "") {
		return Failf(CodeGRPCCreate, "create email http=%d grpc=%s profile=%s", resp.StatusCode, st, c.profile)
	}
	return nil
}

func (c *Client) VerifyEmailCode(email, code string) error {
	code = strings.NewReplacer("-", "", " ", "").Replace(code)
	inner := append(pbStr(1, email), pbStr(2, code)...)
	frame := grpcWebFrame(inner)
	req, err := NewRequest(http.MethodPost, ConnectVerify, bytes.NewReader(frame))
	if err != nil {
		return err
	}
	c.setGRPCHeaders(req)
	resp, err := c.sess.Do(req)
	if err != nil {
		return Wrap(CodeGRPCVerify, "verify email", err)
	}
	body, _ := readAllBody(resp)
	st := readGRPCStatus(resp, body)
	if st == "" {
		st = "0"
	}
	if resp.StatusCode != 200 || (st != "0" && st != "") {
		return Failf(CodeGRPCVerify, "verify email http=%d grpc=%s", resp.StatusCode, st)
	}
	return nil
}

// ValidatePassword optional gRPC step (field 4 email, 5 password). Non-fatal for callers.
func (c *Client) ValidatePassword(email, password string) error {
	inner := append(pbStr(4, email), pbStr(5, password)...)
	frame := grpcWebFrame(inner)
	req, err := NewRequest(http.MethodPost, ConnectPass, bytes.NewReader(frame))
	if err != nil {
		return err
	}
	c.setGRPCHeaders(req)
	resp, err := c.sess.Do(req)
	if err != nil {
		return Wrap(CodeGRPCPassword, "validate password", err)
	}
	body, _ := readAllBody(resp)
	st := readGRPCStatus(resp, body)
	if st == "" {
		st = "0"
	}
	if resp.StatusCode != 200 || (st != "0" && st != "") {
		return Failf(CodeGRPCPassword, "validate password http=%d grpc=%s", resp.StatusCode, st)
	}
	return nil
}

// SignupServerAction posts Next.js server action body; returns response text and SSO cookie if set.
func (c *Client) SignupServerAction(body []byte, actionID, stateTree string) (string, string, error) {
	req, err := NewRequest(http.MethodPost, SignupURLGrok, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	c.setBrowserHeaders(req)
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Next-Action", actionID)
	req.Header.Set("Next-Router-State-Tree", stateTree)
	req.Header.Set("Origin", SiteURL)
	req.Header.Set("Referer", SignupURLGrok)
	resp, err := c.sess.Do(req)
	if err != nil {
		return "", "", Wrap(CodeSignup, "signup action", err)
	}
	text, _ := readBody(resp)

	sso := sessionSSOFromCookies(resp.Cookies())
	if !isSessionSSO(sso) {
		for _, hop := range expandSSOHopURLs(extractAllSetCookieURLs(text)) {
			if v, err := c.followSSOHop(hop); err == nil && isSessionSSO(v) {
				sso = v
				break
			}
		}
	}
	if !isSessionSSO(sso) {
		sso = c.jarSSO()
	}
	if !isSessionSSO(sso) {
		if m := ExtractSSOFromText(text); isSessionSSO(m) {
			sso = m
		}
	}
	if !isSessionSSO(sso) {
		sso = ""
	}
	if resp.StatusCode >= 400 {
		return text, sso, Failf(CodeSignup, "signup http=%d body=%s", resp.StatusCode, truncate(text, 200))
	}
	if sso == "" {
		return text, "", Failf(CodeSignupNoSSO, "signup ok but no session sso hops=%d", len(extractAllSetCookieURLs(text)))
	}
	return text, sso, nil
}

func (c *Client) followSSOHop(start string) (string, error) {
	hops := expandSSOHopURLs([]string{start})
	seen := map[string]struct{}{}
	for i := 0; i < len(hops) && i < 10; i++ {
		hop := hops[i]
		if hop == "" {
			continue
		}
		if _, ok := seen[hop]; ok {
			continue
		}
		seen[hop] = struct{}{}

		req, err := NewRequest(http.MethodGet, hop, nil)
		if err != nil {
			continue
		}
		c.setBrowserHeaders(req)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Referer", SiteURL+"/")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Upgrade-Insecure-Requests", "1")

		resp, err := c.sess.DoNoRedirect(req)
		if err != nil {
			continue
		}
		body, _ := readBody(resp)

		if v := sessionSSOFromCookies(resp.Cookies()); isSessionSSO(v) {
			return v, nil
		}
		if v := ExtractSSOFromText(body); isSessionSSO(v) {
			return v, nil
		}
		if v := c.jarSSO(); isSessionSSO(v) {
			return v, nil
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			continue
		}
		if strings.HasPrefix(loc, "/") {
			if strings.Contains(hop, "grokusercontent") {
				loc = "https://auth.grokusercontent.com" + loc
			} else {
				loc = SiteURL + loc
			}
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.HasPrefix(loc, "http") {
			if _, ok := seen[loc]; !ok {
				hops = append(hops, expandSSOHopURLs([]string{loc})...)
			}
		}
	}
	if v := c.jarSSO(); isSessionSSO(v) {
		return v, nil
	}
	return "", nil
}

func (c *Client) jarSSO() string {
	for _, host := range []string{SiteURL, "https://x.ai", "https://auth.x.ai", "https://grok.com", "https://auth.grokusercontent.com", "https://auth.grokipedia.com"} {
		for _, ck := range c.sess.Cookies(host) {
			if ck.Name == "sso" && isSessionSSO(ck.Value) {
				return ck.Value
			}
		}
	}
	return ""
}

func sessionSSOFromCookies(cks []*http.Cookie) string {
	for _, sc := range cks {
		if sc.Name == "sso" && isSessionSSO(sc.Value) {
			return sc.Value
		}
	}
	return ""
}

func isSessionSSO(tok string) bool {
	if tok == "" || !strings.HasPrefix(tok, "eyJ") || strings.Count(tok, ".") != 2 {
		return false
	}
	payload := jwtPayloadMap(tok)
	if payload == nil {
		return len(tok) > 80
	}
	if cfg, ok := payload["config"].(map[string]any); ok {
		if _, ok := cfg["success_url"]; ok {
			return false
		}
		if _, ok := cfg["token"]; ok {
			return false
		}
	}
	if _, ok := payload["success_url"]; ok {
		return false
	}
	return len(tok) > 40
}

func jwtPayloadMap(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
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
			return nil
		}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

var (
	setCookieURLRe = regexp.MustCompile(
		`https?://[^\s"'<>\\]+set-cookie/?\?q=eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`,
	)
	setCookieRelRe = regexp.MustCompile(
		`(/[A-Za-z0-9_./-]*set-cookie/?\?q=eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+)`,
	)
	jwtRe = regexp.MustCompile(`eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`)
)

func normalizeRSC(text string) string {
	t := text
	t = strings.ReplaceAll(t, `\u0026`, "&")
	t = strings.ReplaceAll(t, `\u003d`, "=")
	t = strings.ReplaceAll(t, `\u002F`, "/")
	t = strings.ReplaceAll(t, `\/`, `/`)
	return t
}

func extractAllSetCookieURLs(text string) []string {
	body := normalizeRSC(text)
	var found []string
	seen := map[string]struct{}{}
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		found = append(found, u)
	}
	for _, m := range setCookieURLRe.FindAllString(body, -1) {
		add(m)
	}
	for _, m := range setCookieRelRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			add(SiteURL + m[1])
		}
	}
	if len(found) == 0 {
		if idx := strings.Index(strings.ToLower(body), "set-cookie"); idx >= 0 {
			window := body[idx:]
			if len(window) > 400 {
				window = window[:400]
			}
			if j := jwtRe.FindString(window); j != "" {
				add("https://auth.grokusercontent.com/set-cookie?q=" + j)
			}
		}
	}
	return found
}

func expandSSOHopURLs(urls []string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(u string) {
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	for _, u := range urls {
		add(u)
		jwt := jwtFromSetCookieURL(u)
		if jwt == "" {
			continue
		}
		if payload := jwtPayloadMap(jwt); payload != nil {
			if cfg, ok := payload["config"].(map[string]any); ok {
				if s, ok := cfg["success_url"].(string); ok && strings.HasPrefix(s, "https://") {
					add(s)
					if strings.Contains(s, "set-cookie") && !strings.Contains(s, "q=") {
						add(strings.TrimRight(s, "/") + "?q=" + jwt)
					}
				}
			}
			if s, ok := payload["success_url"].(string); ok && strings.HasPrefix(s, "https://") {
				add(s)
			}
		}
		add("https://auth.grokusercontent.com/set-cookie?q=" + jwt)
		add("https://auth.grokipedia.com/set-cookie?q=" + jwt)
		add("https://auth.grok.com/set-cookie?q=" + jwt)
		add("https://auth.x.ai/set-cookie?q=" + jwt)
	}
	return out
}

func jwtFromSetCookieURL(u string) string {
	raw, err := url.QueryUnescape(u)
	if err != nil {
		raw = u
	}
	if i := strings.Index(raw, "q="); i >= 0 {
		rest := raw[i+2:]
		if j := strings.IndexAny(rest, "&\"' "); j >= 0 {
			rest = rest[:j]
		}
		if strings.HasPrefix(rest, "eyJ") {
			return rest
		}
	}
	return jwtRe.FindString(raw)
}

func (c *Client) ClearAuthCookies() {
	// New jar via recreate session profile (keep TLS profile)
	_ = c.RecreateWithProfile(c.profile)
	c.applyClearanceCookies()
}

func (c *Client) setBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("sec-ch-ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
}

func (c *Client) setGRPCHeaders(req *http.Request) {
	c.setBrowserHeaders(req)
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")
	req.Header.Set("X-User-Agent", "connect-es/2.1.1")
	req.Header.Set("Origin", SiteURL)
	req.Header.Set("Referer", SignupURLGrok)
	req.Header.Set("Accept", "*/*")
}

var givenNames = []string{
	"James", "John", "Robert", "Michael", "William", "David", "Richard", "Joseph", "Thomas", "Charles",
}
var familyNames = []string{
	"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis", "Rodriguez", "Martinez",
}

// BuildSignupBody builds Next server-action JSON.
// castleToken empty = current production strategy.
func BuildSignupBody(email, password, code, turnstileToken string) []byte {
	return BuildSignupBodyCastle(email, password, code, turnstileToken, "")
}

func BuildSignupBodyCastle(email, password, code, turnstileToken, castleToken string) []byte {
	given := givenNames[mrand.Intn(len(givenNames))]
	family := familyNames[mrand.Intn(len(familyNames))]
	// Document-aligned single-element array + tosAcceptedVersion:1
	payload := []any{
		map[string]any{
			"emailValidationCode": code,
			"createUserAndSessionRequest": map[string]any{
				"email":              email,
				"givenName":          given,
				"familyName":         family,
				"clearTextPassword":  password,
				"tosAcceptedVersion": 1,
			},
			"turnstileToken":     turnstileToken,
			"conversionId":       randomUUID(),
			"castleRequestToken": castleToken,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte("[]")
	}
	return raw
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func pbStr(field int, s string) []byte {
	tag := byte(field<<3 | 2)
	b := []byte(s)
	out := []byte{tag}
	out = append(out, pbVarint(len(b))...)
	out = append(out, b...)
	return out
}

func pbVarint(n int) []byte {
	var parts []byte
	for n > 0x7f {
		parts = append(parts, byte(n&0x7f)|0x80)
		n >>= 7
	}
	parts = append(parts, byte(n))
	return parts
}

func grpcWebFrame(inner []byte) []byte {
	frame := make([]byte, 5+len(inner))
	frame[0] = 0
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(inner)))
	copy(frame[5:], inner)
	return frame
}

func scrapeStateTree(html string) string {
	chunks := flightRe.FindAllStringSubmatch(html, -1)
	for _, ch := range chunks {
		if len(ch) < 2 {
			continue
		}
		decoded := strings.ReplaceAll(ch[1], `\"`, `"`)
		if !strings.Contains(decoded, "sign-up") {
			continue
		}
		idx := strings.Index(decoded, `"f":[[[`)
		if idx < 0 {
			continue
		}
		fStart := idx + 5
		end := strings.Index(decoded[fStart:], `"$undefined"`)
		if end < 0 {
			continue
		}
		raw := decoded[fStart : fStart+end]
		raw = strings.ReplaceAll(raw, `\\"`, `"`)
		raw = strings.ReplaceAll(raw, `\`, "")
		return url.QueryEscape(raw)
	}
	return ""
}

func isCloudflare(status int, body string, h http.Header) bool {
	if status == 403 || status == 503 {
		low := strings.ToLower(body)
		if strings.Contains(low, "cf-") || strings.Contains(low, "cloudflare") || strings.Contains(low, "just a moment") || strings.Contains(low, "attention required") {
			return true
		}
	}
	if h != nil && strings.Contains(strings.ToLower(h.Get("Server")), "cloudflare") && status >= 400 {
		return true
	}
	return false
}

func readBody(resp *http.Response) (string, error) {
	if resp == nil || resp.Body == nil {
		return "", nil
	}
	defer resp.Body.Close()
	var r io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err == nil {
			defer gz.Close()
			r = gz
		}
	}
	b, err := io.ReadAll(io.LimitReader(r, 8<<20))
	return string(b), err
}

func unique(matches [][]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if _, ok := seen[m[1]]; ok {
			continue
		}
		seen[m[1]] = struct{}{}
		out = append(out, m[1])
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ExtractSSOFromText finds an embedded sso=JWT (session) in RSC/HTML body.
func ExtractSSOFromText(text string) string {
	body := normalizeRSC(text)
	reNamed := regexp.MustCompile(`(?i)(?:^|[;,\s'"\\])sso=(eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+)`)
	if m := reNamed.FindStringSubmatch(body); len(m) > 1 && isSessionSSO(m[1]) {
		return m[1]
	}
	reNear := regexp.MustCompile(`(?i)(?:sso|session)[^e]{0,40}(eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+)`)
	if m := reNear.FindStringSubmatch(body); len(m) > 1 && isSessionSSO(m[1]) {
		return m[1]
	}
	for _, m := range jwtRe.FindAllString(body, -1) {
		if isSessionSSO(m) {
			return m
		}
	}
	return ""
}
