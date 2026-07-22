package protocol

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// Session is an HTTP session with optional Chrome TLS/JA3 impersonation.
type Session struct {
	cli     tls_client.HttpClient
	profile string
	proxy   string
	ua      string
	mu      sync.Mutex
}

// NewSession builds a browser-like HTTP client.
// profile examples: chrome_131, chrome_124, chrome_120, chrome (maps to default).
// Empty profile uses chrome_131.
func NewSession(proxy, profile string, timeout time.Duration) (*Session, error) {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	name, prof := resolveProfile(profile)
	jar := tls_client.NewCookieJar()
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(int(timeout.Seconds())),
		tls_client.WithClientProfile(prof),
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithCookieJar(jar),
		// manual redirect control for SSO hops; default follow for normal requests
		tls_client.WithNotFollowRedirects(),
	}
	if strings.TrimSpace(proxy) != "" {
		opts = append(opts, tls_client.WithProxyUrl(strings.TrimSpace(proxy)))
	}
	cli, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, err
	}
	// Enable redirect follow for general use; SSO hop disables per-request via custom client if needed
	cli.SetFollowRedirect(true)
	return &Session{
		cli:     cli,
		profile: name,
		proxy:   strings.TrimSpace(proxy),
		ua:      DefaultUserAgent,
	}, nil
}

func resolveProfile(name string) (string, profiles.ClientProfile) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == "auto" || n == "chrome" {
		n = "chrome_131"
	}
	// normalize chrome131 -> chrome_131
	n = strings.ReplaceAll(n, "-", "_")
	if !strings.Contains(n, "_") && strings.HasPrefix(n, "chrome") && len(n) > 6 {
		// chrome131
		n = "chrome_" + strings.TrimPrefix(n, "chrome")
	}
	if p, ok := profiles.MappedTLSClients[n]; ok {
		return n, p
	}
	// common aliases
	switch n {
	case "chrome131":
		return "chrome_131", profiles.Chrome_131
	case "chrome124":
		return "chrome_124", profiles.Chrome_124
	case "chrome120":
		return "chrome_120", profiles.Chrome_120
	default:
		return "chrome_131", profiles.Chrome_131
	}
}

// ProfileName returns the active impersonation profile key.
func (s *Session) ProfileName() string {
	if s == nil {
		return ""
	}
	return s.profile
}

func (s *Session) SetUserAgent(ua string) {
	if s == nil || strings.TrimSpace(ua) == "" {
		return
	}
	s.mu.Lock()
	s.ua = ua
	s.mu.Unlock()
}

func (s *Session) UserAgent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ua
}

// SetCookies injects cookies for host.
func (s *Session) SetCookies(rawURL string, cookies []*http.Cookie) {
	if s == nil || s.cli == nil {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	s.cli.SetCookies(u, cookies)
}

// Cookies returns jar cookies for URL.
func (s *Session) Cookies(rawURL string) []*http.Cookie {
	if s == nil || s.cli == nil {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return s.cli.GetCookies(u)
}

// Do issues req (fhttp). Caller must close body.
func (s *Session) Do(req *http.Request) (*http.Response, error) {
	if s == nil || s.cli == nil {
		return nil, fmt.Errorf("nil session")
	}
	return s.cli.Do(req)
}

// DoNoRedirect performs request without following redirects.
func (s *Session) DoNoRedirect(req *http.Request) (*http.Response, error) {
	if s == nil || s.cli == nil {
		return nil, fmt.Errorf("nil session")
	}
	prev := s.cli.GetFollowRedirect()
	s.cli.SetFollowRedirect(false)
	defer s.cli.SetFollowRedirect(prev)
	return s.cli.Do(req)
}

// NewRequest builds fhttp request with browser-like defaults applied later by Client.
func NewRequest(method, rawURL string, body io.Reader) (*http.Request, error) {
	return http.NewRequest(method, rawURL, body)
}

// FallbackProfiles returns ordered profile names to try after primary fails CF.
func FallbackProfiles(csv string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name, _ := resolveProfile(p)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{"chrome_124", "chrome_120"}
	}
	return out
}
