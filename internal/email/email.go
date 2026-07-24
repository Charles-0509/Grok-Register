package email

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/grok-free-register/grok-reg/internal/config"
)

var bannedDomains = map[string]struct{}{
	"duckmail.sbs":    {},
	"web-library.net": {},
	"mail.tm":         {},
	"mail.gw":         {},
	"baldur.edu.kg":   {},
}

var codeRe = []*regexp.Regexp{
	regexp.MustCompile(`>([A-Z0-9]{3}-[A-Z0-9]{3})<`),
	regexp.MustCompile(`>([A-Z0-9]{6})<`),
	regexp.MustCompile(`\b([A-Z0-9]{3}-?[A-Z0-9]{3})\b`),
}

type Handle struct {
	Kind     string // lol | mt | custom | testmail | cftemp
	Email    string
	Password string
	Token    string
	Base     string // mail.tm base or cf_temp worker root
	// testmail.app
	Tag       string
	Timestamp int64 // ms — only accept mails after Create()
	// cloudflare_temp_email
	AddressID int64
}

type Provider struct {
	cfg Config
	mu  sync.Mutex
	// lol rate limit
	lolNextOK time.Time
}

type Config struct {
	Mode          config.EmailMode
	Domain        string
	API           string
	LOLRetries    int
	LOLIntervalMS int
	// testmail.app
	TestmailAPIKey    string
	TestmailNamespace string
	TestmailDomain    string
	// cloudflare_temp_email (dreamhunter2333)
	CFTempAPI    string
	CFTempAdmin  string
	CFTempDomain string
	CFTempAuth   string // optional x-custom-auth
	CFTempPrefix bool
	HTTPClient   *http.Client
}

func New(cfg Config) *Provider {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.LOLRetries <= 0 {
		cfg.LOLRetries = 8
	}
	if cfg.LOLIntervalMS <= 0 {
		cfg.LOLIntervalMS = 400
	}
	return &Provider{cfg: cfg}
}

func randStr(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func (p *Provider) Create() (Handle, error) {
	password := randStr(15)
	switch p.cfg.Mode {
	case config.EmailCustom:
		email := fmt.Sprintf("oc%s@%s", randStr(10), p.cfg.Domain)
		return Handle{Kind: "custom", Email: email, Password: password}, nil
	case config.EmailTestmail:
		h, err := p.testmailCreate()
		if err != nil {
			return Handle{}, err
		}
		h.Password = password
		return h, nil
	case config.EmailCFTemp:
		h, err := p.cfTempCreate()
		if err != nil {
			return Handle{}, err
		}
		h.Password = password
		return h, nil
	default:
		// tempmail.lol then mail.tm family
		var last error
		for i := 0; i < p.cfg.LOLRetries; i++ {
			h, err := p.lolCreate()
			if err == nil {
				h.Password = password
				return h, nil
			}
			last = err
			time.Sleep(time.Duration(50*(i+1)) * time.Millisecond)
		}
		for _, base := range []string{"https://api.mail.tm", "https://api.mail.gw", "https://api.duckmail.sbs"} {
			h, err := p.mailtmCreate(base, password)
			if err == nil {
				return h, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("所有临时邮箱 provider 均不可用")
		}
		return Handle{}, last
	}
}

// testmailCreate builds {namespace}.{tag}@{domain} — tags need no pre-registration.
// Docs: https://testmail.app/docs  JSON API livequery + tag filter.
func (p *Provider) testmailCreate() (Handle, error) {
	key := strings.TrimSpace(p.cfg.TestmailAPIKey)
	ns := strings.TrimSpace(p.cfg.TestmailNamespace)
	if key == "" || ns == "" {
		return Handle{}, fmt.Errorf("testmail: set TESTMAIL_API_KEY and TESTMAIL_NAMESPACE")
	}
	dom := strings.TrimSpace(p.cfg.TestmailDomain)
	if dom == "" {
		dom = "inbox.testmail.app"
	}
	tag := "g" + randStr(12)
	email := fmt.Sprintf("%s.%s@%s", ns, tag, dom)
	return Handle{
		Kind:      "testmail",
		Email:     email,
		Tag:       tag,
		Timestamp: time.Now().UnixMilli(),
	}, nil
}

func (p *Provider) lolCreate() (Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Before(p.lolNextOK) {
		time.Sleep(time.Until(p.lolNextOK))
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.tempmail.lol/v2/inbox/create", nil)
	if err != nil {
		return Handle{}, err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return Handle{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data map[string]any
	_ = json.Unmarshal(body, &data)
	if resp.StatusCode == 429 || strings.Contains(strings.ToLower(string(body)), "rate limit") {
		cool := 5 * time.Second
		p.lolNextOK = time.Now().Add(cool)
		return Handle{}, fmt.Errorf("lol rate limited status=%d", resp.StatusCode)
	}
	addr, _ := data["address"].(string)
	tok, _ := data["token"].(string)
	if addr == "" || tok == "" {
		p.lolNextOK = time.Now().Add(800 * time.Millisecond)
		return Handle{}, fmt.Errorf("lol create failed status=%d body=%s", resp.StatusCode, truncate(string(body), 80))
	}
	if domainBanned(addr) {
		p.lolNextOK = time.Now().Add(time.Duration(p.cfg.LOLIntervalMS) * time.Millisecond)
		return Handle{}, fmt.Errorf("lol domain banned: %s", domainOf(addr))
	}
	p.lolNextOK = time.Now().Add(time.Duration(p.cfg.LOLIntervalMS) * time.Millisecond)
	return Handle{Kind: "lol", Email: addr, Token: tok}, nil
}

func (p *Provider) mailtmCreate(base, password string) (Handle, error) {
	resp, err := p.cfg.HTTPClient.Get(base + "/domains")
	if err != nil {
		return Handle{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return Handle{}, err
	}
	members, _ := doc["hydra:member"].([]any)
	var doms []string
	for _, m := range members {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		d, _ := mm["domain"].(string)
		if d == "" || domainBanned(d) {
			continue
		}
		active, _ := mm["isActive"].(bool)
		priv, _ := mm["isPrivate"].(bool)
		if mm["isActive"] != nil && !active {
			continue
		}
		if priv {
			continue
		}
		doms = append(doms, d)
	}
	if len(doms) == 0 {
		return Handle{}, fmt.Errorf("no domain from %s", base)
	}
	rand.Shuffle(len(doms), func(i, j int) { doms[i], doms[j] = doms[j], doms[i] })
	var last error
	for _, dom := range doms {
		if len(doms) > 6 {
			// try at most 6
		}
		email := fmt.Sprintf("oc%s@%s", randStr(10), dom)
		payload := map[string]string{"address": email, "password": password}
		raw, _ := json.Marshal(payload)
		r, err := p.cfg.HTTPClient.Post(base+"/accounts", "application/json", strings.NewReader(string(raw)))
		if err != nil {
			last = err
			continue
		}
		_ = r.Body.Close()
		r2, err := p.cfg.HTTPClient.Post(base+"/token", "application/json", strings.NewReader(string(raw)))
		if err != nil {
			last = err
			continue
		}
		tb, _ := io.ReadAll(io.LimitReader(r2.Body, 1<<20))
		_ = r2.Body.Close()
		var tokDoc map[string]any
		_ = json.Unmarshal(tb, &tokDoc)
		tok, _ := tokDoc["token"].(string)
		if tok == "" {
			last = fmt.Errorf("no token")
			continue
		}
		return Handle{Kind: "mt", Email: email, Password: password, Token: tok, Base: base}, nil
	}
	if last == nil {
		last = fmt.Errorf("mailtm create failed")
	}
	return Handle{}, last
}

func (p *Provider) PollCode(h Handle, maxWait time.Duration) (string, error) {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		text, err := p.fetch(h)
		if err == nil && text != "" {
			if code := extractCode(text); code != "" {
				return code, nil
			}
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("验证码超时")
}

func (p *Provider) fetch(h Handle) (string, error) {
	switch h.Kind {
	case "custom":
		u := strings.TrimRight(p.cfg.API, "/") + "/check/" + url.PathEscape(h.Email)
		resp, err := p.cfg.HTTPClient.Get(u)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("status %d", resp.StatusCode)
		}
		var doc map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&doc)
		if c, _ := doc["code"].(string); c != "" {
			return c, nil
		}
		return "", nil
	case "lol":
		resp, err := p.cfg.HTTPClient.Get("https://api.tempmail.lol/v2/inbox?token=" + url.QueryEscape(h.Token))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		var data map[string]any
		_ = json.Unmarshal(body, &data)
		items, _ := data["emails"].([]any)
		if items == nil {
			items, _ = data["messages"].([]any)
		}
		var b strings.Builder
		for _, it := range items {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			fmt.Fprintf(&b, "%v\n%v\n%v\n", m["subject"], m["body"], m["html"])
		}
		return b.String(), nil
	case "mt":
		req, _ := http.NewRequest(http.MethodGet, h.Base+"/messages", nil)
		req.Header.Set("Authorization", "Bearer "+h.Token)
		req.Header.Set("Accept", "application/json")
		resp, err := p.cfg.HTTPClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		var data map[string]any
		_ = json.Unmarshal(body, &data)
		msgs, _ := data["hydra:member"].([]any)
		if len(msgs) == 0 {
			return "", nil
		}
		m0, _ := msgs[0].(map[string]any)
		id, _ := m0["id"].(string)
		req2, _ := http.NewRequest(http.MethodGet, h.Base+"/messages/"+id, nil)
		req2.Header.Set("Authorization", "Bearer "+h.Token)
		resp2, err := p.cfg.HTTPClient.Do(req2)
		if err != nil {
			return "", err
		}
		defer resp2.Body.Close()
		b2, _ := io.ReadAll(io.LimitReader(resp2.Body, 2<<20))
		return string(b2), nil
	case "testmail":
		return p.testmailFetch(h)
	case "cftemp":
		return p.cfTempFetch(h)
	default:
		return "", fmt.Errorf("unknown handle kind")
	}
}

// cfTempCreate creates a mailbox via cloudflare_temp_email Worker.
// Preferred: POST /admin/new_address with x-admin-auth (no Turnstile).
// Fallback: POST /api/new_address when admin is empty (needs ENABLE_USER_CREATE_EMAIL).
// Ref: https://github.com/dreamhunter2333/cloudflare_temp_email
//
// CF_TEMP_EMAIL_DOMAIN is optional: leave empty to let Worker pick from its
// configured DOMAINS / DEFAULT_DOMAINS (random or first, per Worker env).
func (p *Provider) cfTempCreate() (Handle, error) {
	base := strings.TrimRight(strings.TrimSpace(p.cfg.CFTempAPI), "/")
	if base == "" {
		// allow reuse of EMAIL_API for convenience
		base = strings.TrimRight(strings.TrimSpace(p.cfg.API), "/")
	}
	if base == "" {
		return Handle{}, fmt.Errorf("cf_temp_email: set CF_TEMP_EMAIL_API (Worker root URL)")
	}
	domain := strings.TrimSpace(p.cfg.CFTempDomain)
	if domain == "" {
		domain = strings.TrimSpace(p.cfg.Domain)
	}
	// domain empty → Worker auto-selects from configured domains (random/default)
	name := "oc" + randStr(10)
	payload := map[string]any{
		"name":         name,
		"enablePrefix": p.cfg.CFTempPrefix,
	}
	if domain != "" {
		payload["domain"] = domain
	}
	raw, _ := json.Marshal(payload)

	// 1) admin path
	admin := strings.TrimSpace(p.cfg.CFTempAdmin)
	if admin != "" {
		req, err := http.NewRequest(http.MethodPost, base+"/admin/new_address", strings.NewReader(string(raw)))
		if err != nil {
			return Handle{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-admin-auth", admin)
		if a := strings.TrimSpace(p.cfg.CFTempAuth); a != "" {
			req.Header.Set("x-custom-auth", a)
		}
		resp, err := p.cfg.HTTPClient.Do(req)
		if err != nil {
			return Handle{}, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return p.cfTempParseCreate(body, base)
		}
		return Handle{}, fmt.Errorf("cf_temp_email admin/new_address http=%d body=%s", resp.StatusCode, truncate(string(body), 120))
	}

	// 2) public /api/new_address — if domain still empty, try open_api/settings
	if domain == "" {
		if d, err := p.cfTempPickDomain(base); err == nil && d != "" {
			payload["domain"] = d
			raw, _ = json.Marshal(payload)
		}
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/new_address", strings.NewReader(string(raw)))
	if err != nil {
		return Handle{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a := strings.TrimSpace(p.cfg.CFTempAuth); a != "" {
		req.Header.Set("x-custom-auth", a)
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return Handle{}, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Handle{}, fmt.Errorf("cf_temp_email api/new_address http=%d body=%s (prefer CF_TEMP_EMAIL_ADMIN)", resp.StatusCode, truncate(string(body), 120))
	}
	return p.cfTempParseCreate(body, base)
}

// cfTempPickDomain reads GET /open_api/settings and returns a random domain.
// Used when public create needs an explicit domain and config left it empty.
func (p *Provider) cfTempPickDomain(base string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/open_api/settings", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if a := strings.TrimSpace(p.cfg.CFTempAuth); a != "" {
		req.Header.Set("x-custom-auth", a)
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("settings http=%d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	// prefer defaultDomains, then domains
	var list []string
	for _, key := range []string{"defaultDomains", "domains"} {
		switch v := data[key].(type) {
		case []any:
			for _, it := range v {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					list = append(list, strings.TrimSpace(s))
				}
			}
		case []string:
			for _, s := range v {
				if strings.TrimSpace(s) != "" {
					list = append(list, strings.TrimSpace(s))
				}
			}
		}
		if len(list) > 0 {
			break
		}
	}
	if len(list) == 0 {
		return "", fmt.Errorf("no domains in open_api/settings")
	}
	return list[rand.Intn(len(list))], nil
}

func (p *Provider) cfTempParseCreate(body []byte, base string) (Handle, error) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return Handle{}, fmt.Errorf("cf_temp_email create json: %w body=%s", err, truncate(string(body), 80))
	}
	addr, _ := data["address"].(string)
	jwt, _ := data["jwt"].(string)
	if addr == "" || jwt == "" {
		return Handle{}, fmt.Errorf("cf_temp_email create missing address/jwt: %s", truncate(string(body), 120))
	}
	var aid int64
	switch v := data["address_id"].(type) {
	case float64:
		aid = int64(v)
	case json.Number:
		n, _ := v.Int64()
		aid = n
	}
	return Handle{
		Kind:      "cftemp",
		Email:     addr,
		Token:     jwt,
		Base:      base,
		AddressID: aid,
		Timestamp: time.Now().UnixMilli(),
	}, nil
}

// cfTempFetch pulls mails. Prefers GET /api/parsed_mails (subject/text/html),
// falls back to GET /api/mails (raw RFC822).
func (p *Provider) cfTempFetch(h Handle) (string, error) {
	base := strings.TrimRight(h.Base, "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(p.cfg.CFTempAPI), "/")
	}
	if base == "" || h.Token == "" {
		return "", fmt.Errorf("cf_temp_email not configured")
	}
	// try parsed first
	text, err := p.cfTempGet(base+"/api/parsed_mails?limit=10&offset=0", h.Token, true)
	if err == nil && text != "" {
		return text, nil
	}
	// fallback raw list
	text2, err2 := p.cfTempGet(base+"/api/mails?limit=10&offset=0", h.Token, false)
	if err2 != nil {
		if err != nil {
			return "", fmt.Errorf("cf_temp_email fetch: parsed=%v raw=%v", err, err2)
		}
		return "", err2
	}
	return text2, nil
}

func (p *Provider) cfTempGet(u, jwt string, parsed bool) (string, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/json")
	if a := strings.TrimSpace(p.cfg.CFTempAuth); a != "" {
		req.Header.Set("x-custom-auth", a)
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == 404 && parsed {
		return "", fmt.Errorf("parsed_mails 404")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http=%d body=%s", resp.StatusCode, truncate(string(body), 80))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		// maybe bare array
		var arr []any
		if err2 := json.Unmarshal(body, &arr); err2 == nil {
			return cfTempJoinMails(arr, parsed), nil
		}
		return string(body), nil
	}
	results, _ := data["results"].([]any)
	if results == nil {
		results, _ = data["mails"].([]any)
	}
	if results == nil {
		// single object
		return cfTempJoinMails([]any{data}, parsed), nil
	}
	return cfTempJoinMails(results, parsed), nil
}

func cfTempJoinMails(items []any, parsed bool) string {
	var b strings.Builder
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if parsed {
			fmt.Fprintf(&b, "%v\n%v\n%v\n%v\n", m["subject"], m["text"], m["html"], m["sender"])
		} else {
			fmt.Fprintf(&b, "%v\n%v\n%v\n%v\n%v\n", m["subject"], m["text"], m["html"], m["raw"], m["source"])
		}
	}
	return b.String()
}

func (p *Provider) testmailFetch(h Handle) (string, error) {
	key := strings.TrimSpace(p.cfg.TestmailAPIKey)
	ns := strings.TrimSpace(p.cfg.TestmailNamespace)
	if key == "" || ns == "" {
		return "", fmt.Errorf("testmail not configured")
	}
	// Prefer short poll without livequery (avoids 307 long hangs under proxy).
	q := url.Values{}
	q.Set("apikey", key)
	q.Set("namespace", ns)
	q.Set("tag", h.Tag)
	q.Set("limit", "5")
	if h.Timestamp > 0 {
		q.Set("timestamp_from", fmt.Sprintf("%d", h.Timestamp-2000))
	}
	// Direct to api.testmail.app — do not force register proxy if NO_PROXY includes it;
	// still use HTTPClient which may have proxy from env.
	u := "https://api.testmail.app/api/json?" + q.Encode()
	// Longer timeout client for occasional slow inbox
	client := p.cfg.HTTPClient
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == 429 {
		return "", fmt.Errorf("testmail rate limited")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("testmail http=%d body=%s", resp.StatusCode, truncate(string(body), 80))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	if r, _ := data["result"].(string); r == "fail" {
		msg, _ := data["message"].(string)
		return "", fmt.Errorf("testmail fail: %s", msg)
	}
	emails, _ := data["emails"].([]any)
	var b strings.Builder
	for _, it := range emails {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		fmt.Fprintf(&b, "%v\n%v\n%v\n%v\n", m["subject"], m["text"], m["html"], m["body"])
	}
	return b.String(), nil
}

func extractCode(text string) string {
	for _, re := range codeRe {
		if m := re.FindStringSubmatch(text); len(m) > 1 {
			return strings.ReplaceAll(m[1], "-", "")
		}
	}
	return ""
}

func domainBanned(emailOrDomain string) bool {
	dom := strings.ToLower(strings.TrimSpace(emailOrDomain))
	if i := strings.LastIndexByte(dom, '@'); i >= 0 {
		dom = dom[i+1:]
	}
	if _, ok := bannedDomains[dom]; ok {
		return true
	}
	parts := strings.Split(dom, ".")
	for i := 0; i < len(parts)-1; i++ {
		if _, ok := bannedDomains[strings.Join(parts[i:], ".")]; ok {
			return true
		}
	}
	return false
}

func domainOf(email string) string {
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		return email[i+1:]
	}
	return email
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
