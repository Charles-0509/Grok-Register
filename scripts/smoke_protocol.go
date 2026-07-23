//go:build ignore

// Smoke: TLS impersonate warm GET accounts.x.ai/sign-up
//
//	cd repo && go run scripts/smoke_protocol.go
//	REGISTER_PROXY=http://127.0.0.1:7890 go run scripts/smoke_protocol.go
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/grok-free-register/grok-reg/internal/protocol"
)

func main() {
	proxy := strings.TrimSpace(os.Getenv("REGISTER_PROXY"))
	profiles := []string{"chrome_131", "chrome_124", "chrome_120"}
	if p := strings.TrimSpace(os.Getenv("CF_IMPERSONATE")); p != "" {
		profiles = []string{p}
	}
	fmt.Printf("smoke proxy=%q profiles=%v\n", proxy, profiles)
	okAny := false
	for _, prof := range profiles {
		start := time.Now()
		cli, err := protocol.NewClientOpts(protocol.ClientOptions{
			Proxy:       proxy,
			Impersonate: prof,
			Timeout:     30 * time.Second,
		})
		if err != nil {
			fmt.Printf("  %-12s CREATE_ERR %v\n", prof, err)
			continue
		}
		status, body, err := cli.WarmSignup()
		elapsed := time.Since(start).Round(time.Millisecond)
		if err != nil {
			fmt.Printf("  %-12s ERR status=%d code=%s elapsed=%s err=%v\n",
				prof, status, protocol.CodeOf(err), elapsed, err)
			continue
		}
		cf := strings.Contains(strings.ToLower(body), "cloudflare") && status != 200
		snippet := body
		if len(snippet) > 80 {
			snippet = snippet[:80]
		}
		fmt.Printf("  %-12s HTTP %d bytes=%d cf_hint=%v elapsed=%s body=%q\n",
			prof, status, len(body), cf, elapsed, snippet)
		if status == 200 {
			okAny = true
			cfg, err := cli.FetchConfig()
			if err != nil {
				fmt.Printf("    FetchConfig ERR %v\n", err)
			} else {
				fmt.Printf("    FetchConfig ok sitekey=%s action=%s… source=%s\n",
					cfg.SiteKey, trim(cfg.ActionID, 12), cfg.Source)
			}
		}
	}
	if !okAny {
		fmt.Println("FAIL: no profile got HTTP 200 (try REGISTER_PROXY or clearance)")
		os.Exit(1)
	}
	fmt.Println("OK")
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
