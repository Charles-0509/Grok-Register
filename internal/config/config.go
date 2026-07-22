package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type EmailMode string

const (
	EmailTempmail EmailMode = "tempmail"
	EmailTestmail EmailMode = "testmail"
	EmailCustom   EmailMode = "custom"
)

type Config struct {
	EmailMode   EmailMode
	EmailDomain string
	EmailAPI    string

	// testmail.app (GitHub Student Pack Essential etc.)
	TestmailAPIKey    string
	TestmailNamespace string
	TestmailDomain    string // default inbox.testmail.app

	ClearanceEnabled bool
	RegisterProxy    string
	FlareSolverrURL  string
	ClearanceProxy   string
	ClearanceURLs    string

	Target      int
	PhysicalCap int

	TurnstileProvider string
	LiteSolverURL     string
	// Parallel persistent CloakBrowser workers (browser mint). 0 = auto (min(2, PhysicalCap)).
	TurnstileWorkers int

	ProtocolHTTP bool
	HTTPPoolSize int

	TempmailLOLRetries    int
	TempmailLOLIntervalMS int

	OAuthMinIntervalSec float64
	OAuthRetrySec       float64
	ProbeEnabled        bool

	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string

	// CPA Management upload
	CPAUploadEnabled      bool
	CPAManagementBase     string
	CPAManagementKey      string
	CPAUploadTimeoutSec   int
	CPAUploadRetries      int
	CPAUploadNameTemplate string
	CPAUploadVerify       bool
	CPAUploadMode         string // multipart | json
}

func Defaults() Config {
	return Config{
		EmailMode:             EmailTempmail,
		EmailAPI:              "http://127.0.0.1:8080",
		TestmailDomain:        "inbox.testmail.app",
		ClearanceEnabled:      true,
		RegisterProxy:         "http://127.0.0.1:40080",
		FlareSolverrURL:       "http://127.0.0.1:8191",
		ClearanceProxy:        "http://privoxy:8118",
		ClearanceURLs:         "https://accounts.x.ai,https://x.ai,https://status.x.ai,https://console.x.ai,https://auth.x.ai",
		Target:                10,
		PhysicalCap:           0,
		TurnstileProvider:     "browser",
		LiteSolverURL:         "http://127.0.0.1:5072",
		TurnstileWorkers:      0, // auto
		ProtocolHTTP:          true,
		HTTPPoolSize:          8,
		TempmailLOLRetries:    30,
		TempmailLOLIntervalMS: 1500,
		OAuthMinIntervalSec:   10,
		OAuthRetrySec:         60,
		ProbeEnabled:          true,
		HTTPProxy:             "http://127.0.0.1:40080",
		HTTPSProxy:            "http://127.0.0.1:40080",
		NoProxy:               "127.0.0.1,localhost",
		CPAUploadEnabled:      false,
		CPAManagementBase:     "http://localhost:8317/v0/management",
		CPAUploadTimeoutSec:   30,
		CPAUploadRetries:      2,
		CPAUploadNameTemplate: "{email}.json",
		CPAUploadVerify:       true,
		CPAUploadMode:         "multipart",
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	env := parseEnvFile(string(data))
	applyMap(&cfg, env)
	return cfg, nil
}

func Save(path string, cfg Config) error {
	var b strings.Builder
	b.WriteString("# grok-reg config\n")
	b.WriteString(fmt.Sprintf("EMAIL_MODE=%s\n", cfg.EmailMode))
	if cfg.EmailDomain != "" {
		b.WriteString(fmt.Sprintf("EMAIL_DOMAIN=%s\n", cfg.EmailDomain))
	}
	if cfg.EmailAPI != "" {
		b.WriteString(fmt.Sprintf("EMAIL_API=%s\n", cfg.EmailAPI))
	}
	// testmail secrets: never auto-written (set manually)
	if cfg.TestmailDomain != "" {
		b.WriteString(fmt.Sprintf("TESTMAIL_DOMAIN=%s\n", cfg.TestmailDomain))
	}
	b.WriteString(fmt.Sprintf("CLEARANCE_ENABLED=%s\n", bool01(cfg.ClearanceEnabled)))
	b.WriteString(fmt.Sprintf("REGISTER_PROXY=%s\n", cfg.RegisterProxy))
	b.WriteString(fmt.Sprintf("FLARESOLVERR_URL=%s\n", cfg.FlareSolverrURL))
	b.WriteString(fmt.Sprintf("CLEARANCE_PROXY=%s\n", cfg.ClearanceProxy))
	b.WriteString(fmt.Sprintf("CLEARANCE_URLS=%s\n", cfg.ClearanceURLs))
	b.WriteString(fmt.Sprintf("TURNSTILE_PROVIDER=%s\n", cfg.TurnstileProvider))
	if cfg.LiteSolverURL != "" {
		b.WriteString(fmt.Sprintf("LITE_SOLVER_URL=%s\n", cfg.LiteSolverURL))
	}
	b.WriteString(fmt.Sprintf("TURNSTILE_WORKERS=%d\n", cfg.TurnstileWorkers))
	b.WriteString(fmt.Sprintf("PROTOCOL_HTTP=%s\n", bool01(cfg.ProtocolHTTP)))
	b.WriteString(fmt.Sprintf("HTTP_POOL_SIZE=%d\n", cfg.HTTPPoolSize))
	b.WriteString(fmt.Sprintf("TEMPMAIL_LOL_RETRIES=%d\n", cfg.TempmailLOLRetries))
	b.WriteString(fmt.Sprintf("TEMPMAIL_LOL_MIN_INTERVAL_MS=%d\n", cfg.TempmailLOLIntervalMS))
	b.WriteString(fmt.Sprintf("HTTPS_PROXY=%s\n", cfg.HTTPSProxy))
	b.WriteString(fmt.Sprintf("HTTP_PROXY=%s\n", cfg.HTTPProxy))
	b.WriteString(fmt.Sprintf("NO_PROXY=%s\n", cfg.NoProxy))
	b.WriteString(fmt.Sprintf("PROBE_ENABLED=%s\n", bool01(cfg.ProbeEnabled)))
	b.WriteString(fmt.Sprintf("PHYSICAL_CAP=%d\n", cfg.PhysicalCap))
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_ENABLED=%s\n", bool01(cfg.CPAUploadEnabled)))
	b.WriteString(fmt.Sprintf("CPA_MANAGEMENT_BASE=%s\n", cfg.CPAManagementBase))
	// CPA_MANAGEMENT_KEY: never auto-written (set manually in config.env)
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_TIMEOUT_SEC=%d\n", cfg.CPAUploadTimeoutSec))
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_RETRIES=%d\n", cfg.CPAUploadRetries))
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_NAME_TEMPLATE=%s\n", cfg.CPAUploadNameTemplate))
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func InteractiveSetup(path string) (Config, error) {
	cfg := Defaults()
	fmt.Println()
	fmt.Println("选择邮箱模式:")
	fmt.Println("  [1] 免费临时邮箱           (tempmail.lol · 默认 · 直接回车)")
	fmt.Println("  [2] testmail.app           (GitHub Student Pack Essential 等)")
	fmt.Println("  [3] 自建域名邮箱           (Cloudflare Email Routing + webhook)")
	fmt.Print("输入 1 / 2 / 3 [1]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	switch line {
	case "2":
		cfg.EmailMode = EmailTestmail
		fmt.Print("  TESTMAIL_API_KEY: ")
		key, _ := reader.ReadString('\n')
		cfg.TestmailAPIKey = strings.TrimSpace(key)
		fmt.Print("  TESTMAIL_NAMESPACE: ")
		ns, _ := reader.ReadString('\n')
		cfg.TestmailNamespace = strings.TrimSpace(ns)
		fmt.Print("  TESTMAIL_DOMAIN [inbox.testmail.app]: ")
		dom, _ := reader.ReadString('\n')
		dom = strings.TrimSpace(dom)
		if dom == "" {
			dom = "inbox.testmail.app"
		}
		cfg.TestmailDomain = dom
	case "3":
		cfg.EmailMode = EmailCustom
		fmt.Print("  你的域名 (如 example.com): ")
		dom, _ := reader.ReadString('\n')
		cfg.EmailDomain = strings.TrimSpace(dom)
		fmt.Print("  webhook 地址 [http://127.0.0.1:8080]: ")
		api, _ := reader.ReadString('\n')
		api = strings.TrimSpace(api)
		if api == "" {
			api = "http://127.0.0.1:8080"
		}
		cfg.EmailAPI = api
	default:
		cfg.EmailMode = EmailTempmail
	}
	if err := Save(path, cfg); err != nil {
		return cfg, err
	}
	// Append secrets not written by Save
	if cfg.EmailMode == EmailTestmail {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "TESTMAIL_API_KEY=%s\nTESTMAIL_NAMESPACE=%s\n", cfg.TestmailAPIKey, cfg.TestmailNamespace)
			_ = f.Close()
		}
	}
	fmt.Printf("[*] 已写入 %s\n", path)
	return cfg, nil
}

func ClampTarget(n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("target must be >= 1, got %d", n)
	}
	if n > 10000 {
		return 0, fmt.Errorf("target max is 10000, got %d", n)
	}
	return n, nil
}

func parseEnvFile(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	return out
}

func applyMap(cfg *Config, env map[string]string) {
	if v, ok := env["EMAIL_MODE"]; ok {
		cfg.EmailMode = EmailMode(strings.ToLower(v))
	}
	if v, ok := env["EMAIL_DOMAIN"]; ok {
		cfg.EmailDomain = v
	}
	if v, ok := env["EMAIL_API"]; ok {
		cfg.EmailAPI = v
	}
	if v, ok := env["TESTMAIL_API_KEY"]; ok {
		cfg.TestmailAPIKey = v
	}
	if v, ok := env["TESTMAIL_NAMESPACE"]; ok {
		cfg.TestmailNamespace = v
	}
	if v, ok := env["TESTMAIL_DOMAIN"]; ok {
		cfg.TestmailDomain = v
	}
	if v, ok := env["CLEARANCE_ENABLED"]; ok {
		cfg.ClearanceEnabled = truthy(v)
	}
	if v, ok := env["REGISTER_PROXY"]; ok {
		cfg.RegisterProxy = v
	}
	if v, ok := env["FLARESOLVERR_URL"]; ok {
		cfg.FlareSolverrURL = v
	}
	if v, ok := env["CLEARANCE_PROXY"]; ok {
		cfg.ClearanceProxy = v
	}
	if v, ok := env["CLEARANCE_URLS"]; ok {
		cfg.ClearanceURLs = v
	}
	if v, ok := env["TURNSTILE_PROVIDER"]; ok {
		cfg.TurnstileProvider = v
	}
	if v, ok := env["LITE_SOLVER_URL"]; ok {
		cfg.LiteSolverURL = v
	}
	if v, ok := env["TURNSTILE_WORKERS"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TurnstileWorkers = n
		}
	}
	if v, ok := env["PROTOCOL_HTTP"]; ok {
		cfg.ProtocolHTTP = truthy(v)
	}
	if v, ok := env["HTTP_POOL_SIZE"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.HTTPPoolSize = n
		}
	}
	if v, ok := env["TEMPMAIL_LOL_RETRIES"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TempmailLOLRetries = n
		}
	}
	if v, ok := env["TEMPMAIL_LOL_MIN_INTERVAL_MS"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TempmailLOLIntervalMS = n
		}
	}
	if v, ok := env["HTTPS_PROXY"]; ok {
		cfg.HTTPSProxy = v
	}
	if v, ok := env["HTTP_PROXY"]; ok {
		cfg.HTTPProxy = v
	}
	if v, ok := env["NO_PROXY"]; ok {
		cfg.NoProxy = v
	}
	if v, ok := env["PROBE_ENABLED"]; ok {
		cfg.ProbeEnabled = truthy(v)
	}
	if v, ok := env["PHYSICAL_CAP"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.PhysicalCap = n
		}
	}
	if v, ok := env["CPA_UPLOAD_ENABLED"]; ok {
		cfg.CPAUploadEnabled = truthy(v)
	}
	if v, ok := env["CPA_MANAGEMENT_BASE"]; ok {
		cfg.CPAManagementBase = v
	}
	if v, ok := env["CPA_MANAGEMENT_KEY"]; ok {
		cfg.CPAManagementKey = v
	}
	if v, ok := env["CPA_UPLOAD_TIMEOUT_SEC"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.CPAUploadTimeoutSec = n
		}
	}
	if v, ok := env["CPA_UPLOAD_RETRIES"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.CPAUploadRetries = n
		}
	}
	if v, ok := env["CPA_UPLOAD_NAME_TEMPLATE"]; ok {
		cfg.CPAUploadNameTemplate = v
	}
	if v, ok := env["CPA_UPLOAD_VERIFY"]; ok {
		cfg.CPAUploadVerify = truthy(v)
	}
	if v, ok := env["CPA_UPLOAD_MODE"]; ok {
		cfg.CPAUploadMode = v
	}
}



func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func bool01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ApplyProxyEnv sets process proxy env for outbound HTTP (tempmail etc).
func ApplyProxyEnv(cfg Config) {
	if cfg.HTTPProxy != "" {
		_ = os.Setenv("HTTP_PROXY", cfg.HTTPProxy)
		_ = os.Setenv("http_proxy", cfg.HTTPProxy)
	}
	if cfg.HTTPSProxy != "" {
		_ = os.Setenv("HTTPS_PROXY", cfg.HTTPSProxy)
		_ = os.Setenv("https_proxy", cfg.HTTPSProxy)
	}
	if cfg.NoProxy != "" {
		_ = os.Setenv("NO_PROXY", cfg.NoProxy)
		_ = os.Setenv("no_proxy", cfg.NoProxy)
	}
}
