package config

import (
	"bufio"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed example.env
var embeddedExample string

type EmailMode string

const (
	EmailTempmail EmailMode = "tempmail"
	EmailTestmail EmailMode = "testmail"
	EmailCustom   EmailMode = "custom"
	// EmailCFTemp = dreamhunter2333/cloudflare_temp_email self-hosted Worker
	// Docs: https://github.com/dreamhunter2333/cloudflare_temp_email
	EmailCFTemp EmailMode = "cf_temp_email"
)

type Config struct {
	EmailMode   EmailMode
	EmailDomain string
	EmailAPI    string

	// testmail.app (GitHub Student Pack Essential etc.)
	TestmailAPIKey    string
	TestmailNamespace string
	TestmailDomain    string // default inbox.testmail.app

	// cloudflare_temp_email (self-hosted dreamhunter2333 Worker)
	// CF_TEMP_EMAIL_API   Worker root URL, e.g. https://mail-api.example.com
	// CF_TEMP_EMAIL_ADMIN x-admin-auth password (preferred: POST /admin/new_address)
	// CF_TEMP_EMAIL_DOMAIN optional fixed domain; empty = Worker picks from DOMAINS
	// CF_TEMP_EMAIL_AUTH optional site password (x-custom-auth)
	// CF_TEMP_EMAIL_PREFIX 1=enablePrefix on admin create (default 1)
	CFTempEmailAPI    string
	CFTempEmailAdmin  string
	CFTempEmailDomain string
	CFTempEmailAuth   string
	CFTempEmailPrefix bool

	ClearanceEnabled bool
	// ClearanceMode: auto | always | never
	// auto = protocol TLS first; start stack only on CF block
	// always = EnsureStack when ClearanceEnabled
	// never = never touch docker clearance
	ClearanceMode string
	// ClearanceAutoStop: after run ends or is interrupted, docker compose stop clearance stack.
	ClearanceAutoStop bool
	// ClearanceComposeDir optional override (else GROK_CLEARANCE_DIR / discover).
	ClearanceComposeDir string
	RegisterProxy       string
	FlareSolverrURL     string
	ClearanceProxy      string
	ClearanceURLs       string

	// CF TLS impersonation (bogdanfinn/tls-client profiles)
	CFImpersonate         string // chrome_131
	CFImpersonateFallback string // comma-separated

	// Target / TurnstileWorkers are RUNTIME-ONLY (CLI or interactive start).
	// Not loaded from or saved to config.env.
	Target            int
	PhysicalCap       int
	TurnstileProvider string
	TurnstileMode     string // offscreen | headless | auto
	LiteSolverURL     string
	TurnstileWorkers  int // 1-8 concurrent register/mint threads; set by start

	ProtocolHTTP bool
	HTTPPoolSize int

	TempmailLOLRetries    int
	TempmailLOLIntervalMS int

	OAuthMinIntervalSec float64 // global spacing between OAuth starts (all workers)
	OAuthRetrySec       float64 // rate-limit cooldown base
	OAuthWorkers        int     // concurrent OAuth workers (1–4); 0 = auto
	ProbeEnabled        bool
	ProbeWarmupSec      float64 // sleep before first probe (default 5)

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
		CFTempEmailPrefix:     true,
		ClearanceEnabled:      true,
		ClearanceMode:         "auto",
		ClearanceAutoStop:     true,
		RegisterProxy:         "http://127.0.0.1:40080",
		FlareSolverrURL:       "http://127.0.0.1:8191",
		ClearanceProxy:        "http://privoxy:8118",
		ClearanceURLs:         "https://accounts.x.ai,https://x.ai,https://status.x.ai,https://console.x.ai,https://auth.x.ai",
		CFImpersonate:         "chrome_131",
		CFImpersonateFallback: "chrome_124,chrome_120",
		Target:                0, // set by start CLI/prompt
		PhysicalCap:           0,
		TurnstileProvider:     "browser",
		TurnstileMode:         "offscreen",
		LiteSolverURL:         "http://127.0.0.1:5072",
		TurnstileWorkers:      0, // set by start CLI/prompt
		ProtocolHTTP:          true,
		HTTPPoolSize:          8,
		TempmailLOLRetries:    30,
		TempmailLOLIntervalMS: 1500,
		OAuthMinIntervalSec:   6, // stable: lower 4 easily hits device 429
		OAuthRetrySec:         60,
		OAuthWorkers:          0, // auto: 1 when thread≥3 or large target
		ProbeEnabled:          true,
		ProbeWarmupSec:        5, // stable: new tokens often 403 under 1.5–3s
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

// setEnvKey replaces an active KEY=... line in a .env body (first match).
// If only a commented "# KEY=" exists, uncomment and set it. Otherwise append.
func setEnvKey(content, key, value string) string {
	prefix := key + "="
	commented := "# " + prefix
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, prefix) {
			lines[i] = prefix + value
			found = true
			break
		}
	}
	if !found {
		for i, line := range lines {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, commented) || trim == "#"+prefix || strings.HasPrefix(trim, "#"+prefix) {
				lines[i] = prefix + value
				found = true
				break
			}
		}
	}
	if !found {
		if !strings.HasSuffix(content, "\n") && content != "" {
			lines = append(lines, "")
		}
		lines = append(lines, prefix+value)
	}
	return strings.Join(lines, "\n")
}

// SeedFromExample writes the sectioned Chinese template (embedded example.env)
// to path, then applies overrides (e.g. EMAIL_MODE=tempmail).
func SeedFromExample(path string, overrides map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := embeddedExample
	// Prefer 127.0.0.1 in generated config for host-side CPA default
	content = setEnvKey(content, "CPA_MANAGEMENT_BASE", "http://127.0.0.1:8317/v0/management")
	for k, v := range overrides {
		if k == "" {
			continue
		}
		content = setEnvKey(content, k, v)
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// Save writes a compact key=value config (no section comments).
// Prefer SeedFromExample for first-time user-facing files.
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
	// testmail / cf_temp secrets: never auto-written (set manually)
	if cfg.TestmailDomain != "" {
		b.WriteString(fmt.Sprintf("TESTMAIL_DOMAIN=%s\n", cfg.TestmailDomain))
	}
	if cfg.CFTempEmailAPI != "" {
		b.WriteString(fmt.Sprintf("CF_TEMP_EMAIL_API=%s\n", cfg.CFTempEmailAPI))
	}
	if cfg.CFTempEmailDomain != "" {
		b.WriteString(fmt.Sprintf("CF_TEMP_EMAIL_DOMAIN=%s\n", cfg.CFTempEmailDomain))
	}
	b.WriteString(fmt.Sprintf("CF_TEMP_EMAIL_PREFIX=%s\n", bool01(cfg.CFTempEmailPrefix)))
	b.WriteString(fmt.Sprintf("CLEARANCE_ENABLED=%s\n", bool01(cfg.ClearanceEnabled)))
	if cfg.ClearanceMode != "" {
		b.WriteString(fmt.Sprintf("CLEARANCE_MODE=%s\n", cfg.ClearanceMode))
	}
	b.WriteString(fmt.Sprintf("CLEARANCE_AUTO_STOP=%s\n", bool01(cfg.ClearanceAutoStop)))
	if cfg.ClearanceComposeDir != "" {
		b.WriteString(fmt.Sprintf("CLEARANCE_COMPOSE_DIR=%s\n", cfg.ClearanceComposeDir))
	}
	if cfg.CFImpersonate != "" {
		b.WriteString(fmt.Sprintf("CF_IMPERSONATE=%s\n", cfg.CFImpersonate))
	}
	if cfg.CFImpersonateFallback != "" {
		b.WriteString(fmt.Sprintf("CF_IMPERSONATE_FALLBACK=%s\n", cfg.CFImpersonateFallback))
	}
	b.WriteString(fmt.Sprintf("REGISTER_PROXY=%s\n", cfg.RegisterProxy))
	b.WriteString(fmt.Sprintf("FLARESOLVERR_URL=%s\n", cfg.FlareSolverrURL))
	b.WriteString(fmt.Sprintf("CLEARANCE_PROXY=%s\n", cfg.ClearanceProxy))
	b.WriteString(fmt.Sprintf("CLEARANCE_URLS=%s\n", cfg.ClearanceURLs))
	b.WriteString(fmt.Sprintf("TURNSTILE_PROVIDER=%s\n", cfg.TurnstileProvider))
	if cfg.TurnstileMode != "" {
		b.WriteString(fmt.Sprintf("TURNSTILE_MODE=%s\n", cfg.TurnstileMode))
	}
	if cfg.LiteSolverURL != "" {
		b.WriteString(fmt.Sprintf("LITE_SOLVER_URL=%s\n", cfg.LiteSolverURL))
	}
	// TURNSTILE_WORKERS / TARGET: not persisted — use `grok start -t N --thread M`
	b.WriteString(fmt.Sprintf("PROTOCOL_HTTP=%s\n", bool01(cfg.ProtocolHTTP)))
	b.WriteString(fmt.Sprintf("HTTP_POOL_SIZE=%d\n", cfg.HTTPPoolSize))
	b.WriteString(fmt.Sprintf("TEMPMAIL_LOL_RETRIES=%d\n", cfg.TempmailLOLRetries))
	b.WriteString(fmt.Sprintf("TEMPMAIL_LOL_MIN_INTERVAL_MS=%d\n", cfg.TempmailLOLIntervalMS))
	b.WriteString(fmt.Sprintf("HTTPS_PROXY=%s\n", cfg.HTTPSProxy))
	b.WriteString(fmt.Sprintf("HTTP_PROXY=%s\n", cfg.HTTPProxy))
	b.WriteString(fmt.Sprintf("NO_PROXY=%s\n", cfg.NoProxy))
	b.WriteString(fmt.Sprintf("PROBE_ENABLED=%s\n", bool01(cfg.ProbeEnabled)))
	b.WriteString(fmt.Sprintf("PROBE_WARMUP_SEC=%g\n", cfg.ProbeWarmupSec))
	b.WriteString(fmt.Sprintf("OAUTH_MIN_INTERVAL_SEC=%g\n", cfg.OAuthMinIntervalSec))
	b.WriteString(fmt.Sprintf("OAUTH_RETRY_SEC=%g\n", cfg.OAuthRetrySec))
	b.WriteString(fmt.Sprintf("OAUTH_WORKERS=%d\n", cfg.OAuthWorkers))
	b.WriteString(fmt.Sprintf("PHYSICAL_CAP=%d\n", cfg.PhysicalCap))
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_ENABLED=%s\n", bool01(cfg.CPAUploadEnabled)))
	b.WriteString(fmt.Sprintf("CPA_MANAGEMENT_BASE=%s\n", cfg.CPAManagementBase))
	// CPA_MANAGEMENT_KEY: never auto-written (set manually in config.env)
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_TIMEOUT_SEC=%d\n", cfg.CPAUploadTimeoutSec))
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_RETRIES=%d\n", cfg.CPAUploadRetries))
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_NAME_TEMPLATE=%s\n", cfg.CPAUploadNameTemplate))
	b.WriteString(fmt.Sprintf("CPA_UPLOAD_VERIFY=%s\n", bool01(cfg.CPAUploadVerify)))
	if cfg.CPAUploadMode != "" {
		b.WriteString(fmt.Sprintf("CPA_UPLOAD_MODE=%s\n", cfg.CPAUploadMode))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func InteractiveSetup(path string) (Config, error) {
	cfg := Defaults()
	fmt.Println()
	fmt.Println("选择邮箱模式:")
	fmt.Println("  [1] 免费临时邮箱           (tempmail.lol · 默认 · 直接回车)")
	fmt.Println("  [2] testmail.app           (GitHub Student Pack Essential 等)")
	fmt.Println("  [3] 自建域名 webhook       (Cloudflare Email Routing + 本地 webhook)")
	fmt.Println("  [4] cloudflare_temp_email  (dreamhunter2333 自建 Worker API)")
	fmt.Print("输入 1 / 2 / 3 / 4 [1]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	overrides := map[string]string{
		"EMAIL_MODE": string(EmailTempmail),
	}
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
		overrides["EMAIL_MODE"] = string(EmailTestmail)
		overrides["TESTMAIL_API_KEY"] = cfg.TestmailAPIKey
		overrides["TESTMAIL_NAMESPACE"] = cfg.TestmailNamespace
		overrides["TESTMAIL_DOMAIN"] = cfg.TestmailDomain
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
		overrides["EMAIL_MODE"] = string(EmailCustom)
		overrides["EMAIL_DOMAIN"] = cfg.EmailDomain
		overrides["EMAIL_API"] = cfg.EmailAPI
	case "4":
		cfg.EmailMode = EmailCFTemp
		fmt.Print("  CF_TEMP_EMAIL_API (Worker 根地址, https://...): ")
		api, _ := reader.ReadString('\n')
		cfg.CFTempEmailAPI = strings.TrimRight(strings.TrimSpace(api), "/")
		fmt.Print("  CF_TEMP_EMAIL_ADMIN (x-admin-auth): ")
		adm, _ := reader.ReadString('\n')
		cfg.CFTempEmailAdmin = strings.TrimSpace(adm)
		fmt.Print("  CF_TEMP_EMAIL_DOMAIN (可选，回车=Worker 已配置域名随机): ")
		dom, _ := reader.ReadString('\n')
		cfg.CFTempEmailDomain = strings.TrimSpace(dom)
		fmt.Print("  CF_TEMP_EMAIL_AUTH (可选 x-custom-auth, 回车跳过): ")
		auth, _ := reader.ReadString('\n')
		cfg.CFTempEmailAuth = strings.TrimSpace(auth)
		cfg.CFTempEmailPrefix = true
		overrides["EMAIL_MODE"] = string(EmailCFTemp)
		overrides["CF_TEMP_EMAIL_API"] = cfg.CFTempEmailAPI
		overrides["CF_TEMP_EMAIL_DOMAIN"] = cfg.CFTempEmailDomain
		overrides["CF_TEMP_EMAIL_PREFIX"] = "1"
	default:
		cfg.EmailMode = EmailTempmail
		overrides["EMAIL_MODE"] = string(EmailTempmail)
	}
	// Full sectioned Chinese template (includes CPA_MANAGEMENT_KEY= placeholder)
	if err := SeedFromExample(path, overrides); err != nil {
		return cfg, err
	}
	// Append secrets not written into example overrides as empty placeholders
	if cfg.EmailMode == EmailTestmail && cfg.TestmailAPIKey != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "\n# secrets (append)\nTESTMAIL_API_KEY=%s\nTESTMAIL_NAMESPACE=%s\n", cfg.TestmailAPIKey, cfg.TestmailNamespace)
			_ = f.Close()
		}
	}
	if cfg.EmailMode == EmailCFTemp {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "\n# secrets (append — not rewritten by Save)\n")
			if cfg.CFTempEmailAdmin != "" {
				fmt.Fprintf(f, "CF_TEMP_EMAIL_ADMIN=%s\n", cfg.CFTempEmailAdmin)
			}
			if cfg.CFTempEmailAuth != "" {
				fmt.Fprintf(f, "CF_TEMP_EMAIL_AUTH=%s\n", cfg.CFTempEmailAuth)
			}
			_ = f.Close()
		}
	}
	fmt.Printf("[*] 已写入分区注释配置 %s\n", path)
	fmt.Printf("[*] 参考模板也会同步到同目录 config.env.example\n")
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

// ClampThreads limits concurrent register/mint threads to 1–8.
func ClampThreads(n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("thread must be >= 1, got %d", n)
	}
	if n > 8 {
		return 0, fmt.Errorf("thread max is 8, got %d", n)
	}
	return n, nil
}

// SyncExample writes/updates ~/.grok/config.env.example from the embedded template
// so users see newly added keys after upgrades.
func SyncExample(homeDir string) error {
	if homeDir == "" {
		return fmt.Errorf("empty home dir")
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(homeDir, "config.env.example")
	return os.WriteFile(path, []byte(embeddedExample), 0o644)
}

// ExamplePath returns path to user-local example file.
func ExamplePath(homeDir string) string {
	return filepath.Join(homeDir, "config.env.example")
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
		mode := strings.ToLower(strings.TrimSpace(v))
		// convenience aliases for cloudflare_temp_email
		switch mode {
		case "cf_temp", "cf-temp", "cftemp", "cloudflare_temp_email", "cloudflare-temp-email":
			cfg.EmailMode = EmailCFTemp
		default:
			cfg.EmailMode = EmailMode(mode)
		}
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
	// cloudflare_temp_email
	if v, ok := env["CF_TEMP_EMAIL_API"]; ok {
		cfg.CFTempEmailAPI = strings.TrimRight(strings.TrimSpace(v), "/")
	} else if v, ok := env["CFTEMP_API"]; ok {
		cfg.CFTempEmailAPI = strings.TrimRight(strings.TrimSpace(v), "/")
	}
	if v, ok := env["CF_TEMP_EMAIL_ADMIN"]; ok {
		cfg.CFTempEmailAdmin = v
	} else if v, ok := env["CFTEMP_ADMIN"]; ok {
		cfg.CFTempEmailAdmin = v
	}
	if v, ok := env["CF_TEMP_EMAIL_DOMAIN"]; ok {
		cfg.CFTempEmailDomain = v
	} else if v, ok := env["CFTEMP_DOMAIN"]; ok {
		cfg.CFTempEmailDomain = v
	}
	if v, ok := env["CF_TEMP_EMAIL_AUTH"]; ok {
		cfg.CFTempEmailAuth = v
	} else if v, ok := env["CFTEMP_AUTH"]; ok {
		cfg.CFTempEmailAuth = v
	}
	if v, ok := env["CF_TEMP_EMAIL_PREFIX"]; ok {
		cfg.CFTempEmailPrefix = truthy(v)
	}
	if v, ok := env["CLEARANCE_ENABLED"]; ok {
		cfg.ClearanceEnabled = truthy(v)
	}
	if v, ok := env["CLEARANCE_MODE"]; ok {
		cfg.ClearanceMode = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := env["CLEARANCE_AUTO_STOP"]; ok {
		cfg.ClearanceAutoStop = truthy(v)
	}
	if v, ok := env["CLEARANCE_COMPOSE_DIR"]; ok {
		cfg.ClearanceComposeDir = strings.TrimSpace(v)
	}
	if v, ok := env["CF_IMPERSONATE"]; ok {
		cfg.CFImpersonate = strings.TrimSpace(v)
	}
	if v, ok := env["CF_IMPERSONATE_FALLBACK"]; ok {
		cfg.CFImpersonateFallback = strings.TrimSpace(v)
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
	if v, ok := env["TURNSTILE_MODE"]; ok {
		cfg.TurnstileMode = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := env["LITE_SOLVER_URL"]; ok {
		cfg.LiteSolverURL = v
	}
	// TURNSTILE_WORKERS / TARGET intentionally ignored from env (CLI-only).
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
	if v, ok := env["PROBE_WARMUP_SEC"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.ProbeWarmupSec = f
		}
	}
	if v, ok := env["OAUTH_MIN_INTERVAL_SEC"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.OAuthMinIntervalSec = f
		}
	}
	if v, ok := env["OAUTH_RETRY_SEC"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.OAuthRetrySec = f
		}
	}
	if v, ok := env["OAUTH_WORKERS"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.OAuthWorkers = n
		}
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
