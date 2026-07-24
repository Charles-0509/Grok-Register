// Package reoauth re-issues CPA credentials from inspection exports, CPA JSON, or SSO files.
package reoauth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/grok-free-register/grok-reg/internal/cpa"
	"github.com/grok-free-register/grok-reg/internal/oauth"
)

// Account is one re-login candidate.
type Account struct {
	Email        string
	Password     string // optional
	SSO          string // session JWT cookie
	RefreshToken string
	AccessToken  string // optional existing
	Source       string // path / reason tag
	Sub          string
	IDToken      string
	TokenEP      string
}

// Result is one account outcome.
type Result struct {
	Email  string
	OK     bool
	Method string // refresh | device | skip
	Path   string // written CPA path
	Err    string
}

// Options for Run.
type Options struct {
	Proxy       string
	OutCPA      string
	OutLog      func(format string, args ...any)
	Workers     int
	MinInterval time.Duration
	Probe       bool
	ProbeWarmup float64
	LookupRoots []string // optional ~/.grok/outputs etc. to resolve email→token
	Secret      []byte
}

func logf(opt Options, f string, a ...any) {
	if opt.OutLog != nil {
		opt.OutLog(f, a...)
	}
}

// ParsePath loads accounts from inspection JSON, CPA JSON, accounts.txt, auth-sessions.jsonl, or a directory.
func ParsePath(path string) ([]Account, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return parseDir(path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(filepath.Base(path))
	// accounts.txt style
	if name == "accounts.txt" || looksLikeAccountsTXT(raw) {
		return parseAccountsTXT(path, raw)
	}
	// jsonl sessions
	if strings.HasSuffix(name, ".jsonl") || looksLikeJSONL(raw) {
		return parseAuthSessions(path, raw)
	}
	// JSON object/array
	trim := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
		return parseJSON(path, raw)
	}
	// fallback line-oriented email:pass:sso
	return parseAccountsTXT(path, raw)
}

func looksLikeAccountsTXT(raw []byte) bool {
	s := string(raw)
	if len(s) > 4000 {
		s = s[:4000]
	}
	lines := 0
	hits := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines++
		parts := strings.SplitN(line, ":", 3)
		if len(parts) >= 3 && strings.Contains(parts[0], "@") {
			hits++
		}
		if lines >= 5 {
			break
		}
	}
	return hits >= 1 && hits*2 >= lines
}

func looksLikeJSONL(raw []byte) bool {
	s := strings.TrimSpace(string(raw))
	return strings.HasPrefix(s, "{") && strings.Contains(s, "\n{")
}

func parseDir(dir string) ([]Account, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Account
	// prefer CPA jsons
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		low := strings.ToLower(n)
		if !strings.HasSuffix(low, ".json") {
			continue
		}
		p := filepath.Join(dir, n)
		accs, err := ParsePath(p)
		if err != nil {
			continue
		}
		out = append(out, accs...)
	}
	// also accounts.txt / sessions in dir or SSO subdir
	for _, rel := range []string{"accounts.txt", "SSO/accounts.txt", "auth-sessions.jsonl", "SSO/auth-sessions.jsonl"} {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			accs, err := ParsePath(p)
			if err == nil {
				out = append(out, accs...)
			}
		}
	}
	// nested CPA/
	cpaDir := filepath.Join(dir, "CPA")
	if st, err := os.Stat(cpaDir); err == nil && st.IsDir() {
		more, err := parseDir(cpaDir)
		if err == nil {
			out = append(out, more...)
		}
	}
	return dedupeAccounts(out), nil
}

func parseAccountsTXT(path string, raw []byte) ([]Account, error) {
	var out []Account
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	// long SSO lines
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 || !strings.Contains(parts[0], "@") {
			continue
		}
		out = append(out, Account{
			Email:    strings.TrimSpace(parts[0]),
			Password: parts[1],
			SSO:      strings.TrimSpace(parts[2]),
			Source:   path,
		})
	}
	return out, sc.Err()
}

func parseAuthSessions(path string, raw []byte) ([]Account, error) {
	var out []Account
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var doc map[string]any
		if json.Unmarshal([]byte(line), &doc) != nil {
			continue
		}
		email, _ := doc["email"].(string)
		sso := ""
		if cks, ok := doc["cookies"].([]any); ok {
			for _, c := range cks {
				m, _ := c.(map[string]any)
				if m == nil {
					continue
				}
				if n, _ := m["name"].(string); n == "sso" {
					sso, _ = m["value"].(string)
				}
			}
		}
		if sso == "" {
			sso, _ = doc["sso"].(string)
		}
		if email == "" && sso == "" {
			continue
		}
		out = append(out, Account{Email: email, SSO: sso, Source: path})
	}
	return out, sc.Err()
}

func parseJSON(path string, raw []byte) ([]Account, error) {
	// 1) CPA document
	var cpaDoc cpa.Document
	if err := json.Unmarshal(raw, &cpaDoc); err == nil && (cpaDoc.RefreshToken != "" || cpaDoc.AccessToken != "") {
		return []Account{{
			Email:        cpaDoc.Email,
			RefreshToken: cpaDoc.RefreshToken,
			AccessToken:  cpaDoc.AccessToken,
			Sub:          cpaDoc.Sub,
			IDToken:      cpaDoc.IDToken,
			TokenEP:      cpaDoc.TokenEndpoint,
			Source:       path,
		}}, nil
	}

	// 2) inspection export: { results: [ {email, ...}, ... ] }
	var wrap struct {
		Results []map[string]any `json:"results"`
		Count   int              `json:"count"`
		Filter  string           `json:"filter"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Results) > 0 {
		var out []Account
		for _, r := range wrap.Results {
			email := firstString(r, "email", "name", "file_id")
			email = strings.TrimSuffix(email, ".json")
			if email == "" || !strings.Contains(email, "@") {
				continue
			}
			// optional tokens if present in richer exports
			out = append(out, Account{
				Email:        email,
				RefreshToken: firstString(r, "refresh_token", "refreshToken"),
				AccessToken:  firstString(r, "access_token", "accessToken"),
				SSO:          firstString(r, "sso", "sso_token", "session"),
				Sub:          firstString(r, "sub", "user_id", "userId"),
				Source:       path + "#" + firstString(r, "auth_index", "file_name", email),
			})
		}
		return out, nil
	}

	// 3) array of objects
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		var out []Account
		for _, r := range arr {
			email := firstString(r, "email", "name")
			if email == "" {
				continue
			}
			out = append(out, Account{
				Email:        email,
				RefreshToken: firstString(r, "refresh_token", "refreshToken"),
				AccessToken:  firstString(r, "access_token", "accessToken"),
				SSO:          firstString(r, "sso"),
				Sub:          firstString(r, "sub"),
				Source:       path,
			})
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	// 4) single map with email list
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		if emails, ok := m["emails"].([]any); ok {
			var out []Account
			for _, e := range emails {
				if s, ok := e.(string); ok && strings.Contains(s, "@") {
					out = append(out, Account{Email: s, Source: path})
				}
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}

	return nil, fmt.Errorf("无法解析账号文件: %s（支持 inspection JSON / CPA JSON / accounts.txt / auth-sessions.jsonl）", path)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			}
		}
	}
	return ""
}

func dedupeAccounts(in []Account) []Account {
	seen := map[string]int{}
	var out []Account
	for _, a := range in {
		key := strings.ToLower(strings.TrimSpace(a.Email))
		if key == "" {
			key = a.RefreshToken
			if key == "" {
				key = a.SSO
			}
		}
		if key == "" {
			continue
		}
		if i, ok := seen[key]; ok {
			// merge richer fields into existing
			out[i] = mergeAccount(out[i], a)
			continue
		}
		seen[key] = len(out)
		out = append(out, a)
	}
	return out
}

func mergeAccount(a, b Account) Account {
	if a.RefreshToken == "" {
		a.RefreshToken = b.RefreshToken
	}
	if a.SSO == "" {
		a.SSO = b.SSO
	}
	if a.AccessToken == "" {
		a.AccessToken = b.AccessToken
	}
	if a.Password == "" {
		a.Password = b.Password
	}
	if a.Sub == "" {
		a.Sub = b.Sub
	}
	if a.Source == "" {
		a.Source = b.Source
	}
	return a
}

// EnrichFromOutputs fills missing refresh/sso by scanning lookup roots (outputs/*).
func EnrichFromOutputs(accs []Account, roots []string) []Account {
	if len(roots) == 0 {
		return accs
	}
	byEmail := map[string]Account{}
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			base := strings.ToLower(info.Name())
			// limit depth cost: only known names / cpa json
			if base != "accounts.txt" && base != "auth-sessions.jsonl" &&
				!(strings.HasPrefix(base, "xai-") && strings.HasSuffix(base, ".json")) &&
				!(strings.Contains(path, string(filepath.Separator)+"CPA"+string(filepath.Separator)) && strings.HasSuffix(base, ".json")) {
				return nil
			}
			// skip huge trees beyond outputs/*/*
			rel, _ := filepath.Rel(root, path)
			if strings.Count(rel, string(filepath.Separator)) > 4 {
				return nil
			}
			parsed, err := ParsePath(path)
			if err != nil {
				return nil
			}
			for _, a := range parsed {
				em := strings.ToLower(strings.TrimSpace(a.Email))
				if em == "" {
					continue
				}
				if prev, ok := byEmail[em]; ok {
					byEmail[em] = mergeAccount(prev, a)
				} else {
					byEmail[em] = a
				}
			}
			return nil
		})
	}
	for i := range accs {
		em := strings.ToLower(strings.TrimSpace(accs[i].Email))
		if em == "" {
			continue
		}
		if found, ok := byEmail[em]; ok {
			accs[i] = mergeAccount(accs[i], found)
		}
	}
	return accs
}

// Run re-auths all accounts, writes CPA JSON to OutCPA.
func Run(ctx context.Context, accs []Account, opt Options) ([]Result, error) {
	if opt.Workers <= 0 {
		opt.Workers = 2
	}
	if opt.Workers > 8 {
		opt.Workers = 8
	}
	if opt.MinInterval <= 0 {
		opt.MinInterval = 2 * time.Second
	}
	if len(opt.Secret) == 0 {
		opt.Secret = cpa.DefaultSecret()
	}
	if opt.OutCPA != "" {
		if err := os.MkdirAll(opt.OutCPA, 0o700); err != nil {
			return nil, err
		}
	}

	// Enrich missing credentials from local history
	if len(opt.LookupRoots) > 0 {
		logf(opt, "查找本地 outputs 以补全 refresh/sso（roots=%d）…", len(opt.LookupRoots))
		accs = EnrichFromOutputs(accs, opt.LookupRoots)
	}

	type job struct {
		a Account
		i int
	}
	jobs := make(chan job, len(accs))
	results := make([]Result, len(accs))
	var wg sync.WaitGroup
	var gateMu sync.Mutex
	var nextAt time.Time
	var okN, failN, skipN atomic.Int64

	worker := func() {
		defer wg.Done()
		cli, err := oauth.NewClient(opt.Proxy, nil, 60*time.Second)
		if err != nil {
			logf(opt, "oauth client: %v", err)
			return
		}
		for j := range jobs {
			select {
			case <-ctx.Done():
				results[j.i] = Result{Email: j.a.Email, OK: false, Method: "skip", Err: ctx.Err().Error()}
				skipN.Add(1)
				continue
			default:
			}
			// pace
			gateMu.Lock()
			wait := time.Until(nextAt)
			if wait > 0 {
				gateMu.Unlock()
				select {
				case <-ctx.Done():
					results[j.i] = Result{Email: j.a.Email, OK: false, Method: "skip", Err: ctx.Err().Error()}
					skipN.Add(1)
					continue
				case <-time.After(wait):
				}
				gateMu.Lock()
			}
			nextAt = time.Now().Add(opt.MinInterval)
			gateMu.Unlock()

			res := reauthOne(ctx, cli, j.a, opt)
			results[j.i] = res
			if res.OK {
				okN.Add(1)
				logf(opt, "✓ %s method=%s → %s", res.Email, res.Method, filepath.Base(res.Path))
			} else if res.Method == "skip" {
				skipN.Add(1)
				logf(opt, "– skip %s: %s", res.Email, res.Err)
			} else {
				failN.Add(1)
				logf(opt, "! fail %s method=%s: %s", res.Email, res.Method, res.Err)
			}
		}
	}

	n := opt.Workers
	if n > len(accs) {
		n = len(accs)
	}
	if n < 1 {
		n = 1
	}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go worker()
	}
	for i, a := range accs {
		jobs <- job{a: a, i: i}
	}
	close(jobs)
	wg.Wait()

	logf(opt, "完成 ok=%d fail=%d skip=%d total=%d out=%s", okN.Load(), failN.Load(), skipN.Load(), len(accs), opt.OutCPA)
	return results, nil
}

func reauthOne(ctx context.Context, cli *oauth.Client, a Account, opt Options) Result {
	email := strings.TrimSpace(a.Email)
	res := Result{Email: email}

	var cred oauth.Credential
	var err error
	method := ""

	if strings.TrimSpace(a.RefreshToken) != "" {
		method = "refresh"
		cred, err = cli.Refresh(ctx, a.RefreshToken)
	} else if strings.TrimSpace(a.SSO) != "" {
		method = "device"
		cred, err = cli.Exchange(ctx, a.SSO)
	} else {
		res.Method = "skip"
		res.Err = "无 refresh_token 且无 sso（inspection 仅含 email 时请提供本地 outputs 或 CPA/accounts）"
		return res
	}
	res.Method = method
	if err != nil {
		// if refresh failed, try device when sso present
		if method == "refresh" && strings.TrimSpace(a.SSO) != "" {
			logf(opt, "  %s refresh 失败，回退 device…", email)
			cred, err = cli.Exchange(ctx, a.SSO)
			res.Method = "device"
		}
	}
	if err != nil {
		res.Err = err.Error()
		return res
	}

	doc := cpa.FromCredential(cred, email)
	if opt.Probe {
		warm := opt.ProbeWarmup
		if warm == 0 {
			warm = 2
		}
		if err := cpa.Probe(doc, opt.Proxy, warm); err != nil {
			// still write on rate/exhausted — token is valid
			low := strings.ToLower(err.Error())
			if !strings.Contains(low, "rate") && !strings.Contains(low, "exhaust") {
				res.Err = "probe: " + err.Error()
				// still save? yes for reoauth intent
			}
		}
	}

	if opt.OutCPA == "" {
		res.OK = true
		return res
	}
	path, err := cpa.WriteAtomic(opt.OutCPA, doc, opt.Secret)
	if err != nil {
		res.Err = "write: " + err.Error()
		return res
	}
	res.OK = true
	res.Path = path
	return res
}
