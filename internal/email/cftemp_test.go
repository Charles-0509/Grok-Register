package email

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grok-free-register/grok-reg/internal/config"
)

func TestCFTempCreateAndFetch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/new_address", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-admin-auth") != "secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// domain may be omitted → Worker would auto-pick; test accepts either
		if body["name"] == nil || body["name"] == "" {
			http.Error(w, "need name", 400)
			return
		}
		addr := "oc123@example.com"
		if d, ok := body["domain"].(string); ok && d != "" {
			addr = "oc123@" + d
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jwt":        "jwt-token-abc",
			"address":    addr,
			"address_id": 42,
		})
	})
	mux.HandleFunc("/api/parsed_mails", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer jwt-token-abc") {
			http.Error(w, "no jwt", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{
					"subject": "Your code",
					"text":    "Code is ABC-123",
					"html":    "<b>ABC-123</b>",
					"sender":  "noreply@x.ai",
				},
			},
			"count": 1,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// with explicit domain
	p := New(Config{
		Mode:         config.EmailCFTemp,
		CFTempAPI:    srv.URL,
		CFTempAdmin:  "secret",
		CFTempDomain: "example.com",
		CFTempPrefix: true,
		HTTPClient:   srv.Client(),
	})
	h, err := p.Create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.Kind != "cftemp" || h.Token != "jwt-token-abc" {
		t.Fatalf("handle=%+v", h)
	}
	// without domain (Worker auto-pick)
	p2 := New(Config{
		Mode:        config.EmailCFTemp,
		CFTempAPI:   srv.URL,
		CFTempAdmin: "secret",
		HTTPClient:  srv.Client(),
	})
	h2, err := p2.Create()
	if err != nil {
		t.Fatalf("create no domain: %v", err)
	}
	if h2.Email == "" || h2.Token == "" {
		t.Fatalf("handle2=%+v", h2)
	}
	text, err := p.fetch(h)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	code := extractCode(text)
	if code != "ABC123" {
		t.Fatalf("code=%q text=%q", code, text)
	}
	code2, err := p.PollCode(h, 3*time.Second)
	if err != nil || code2 != "ABC123" {
		t.Fatalf("poll code=%q err=%v", code2, err)
	}
	_ = io.Discard
}

func TestCFTempModeAliases(t *testing.T) {
	// config package aliases are tested via Load path in config — smoke mode const
	if config.EmailCFTemp != "cf_temp_email" {
		t.Fatal(config.EmailCFTemp)
	}
}
