package reoauth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseInspectionJSON(t *testing.T) {
	raw := `{
  "filter": "quota_exhausted",
  "count": 2,
  "results": [
    {"email": "a@example.com", "auth_index": "1", "classification": "quota_exhausted"},
    {"name": "b@example.com", "file_name": "b@example.com.json"}
  ]
}`
	dir := t.TempDir()
	p := filepath.Join(dir, "insp.json")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	accs, err := ParsePath(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 2 {
		t.Fatalf("want 2 got %d", len(accs))
	}
	if accs[0].Email != "a@example.com" || accs[1].Email != "b@example.com" {
		t.Fatalf("emails %+v", accs)
	}
}

func TestParseInspectionJSONBOM(t *testing.T) {
	body := `{"filter":"quota_exhausted","count":1,"results":[{"email":"c@example.com"}]}`
	raw := append([]byte{0xef, 0xbb, 0xbf}, []byte(body)...)
	dir := t.TempDir()
	p := filepath.Join(dir, "bom.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	accs, err := ParsePath(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 || accs[0].Email != "c@example.com" {
		t.Fatalf("%+v", accs)
	}
}

func TestParseCPAJSON(t *testing.T) {
	raw := `{
  "type": "xai",
  "access_token": "at",
  "refresh_token": "rt-value",
  "email": "c@example.com",
  "sub": "sub1",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`
	dir := t.TempDir()
	p := filepath.Join(dir, "xai-test.json")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	accs, err := ParsePath(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 || accs[0].RefreshToken != "rt-value" || accs[0].Email != "c@example.com" {
		t.Fatalf("%+v", accs)
	}
}

func TestParseAccountsTXT(t *testing.T) {
	raw := "u@x.ai:pass:eyJsso\n# comment\n"
	dir := t.TempDir()
	p := filepath.Join(dir, "accounts.txt")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	accs, err := ParsePath(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 || accs[0].SSO != "eyJsso" {
		t.Fatalf("%+v", accs)
	}
}

func TestDedupeMerge(t *testing.T) {
	out := dedupeAccounts([]Account{
		{Email: "A@x.ai", SSO: "s1"},
		{Email: "a@x.ai", RefreshToken: "r1"},
	})
	if len(out) != 1 {
		t.Fatalf("dedupe %d", len(out))
	}
	if out[0].SSO != "s1" || out[0].RefreshToken != "r1" {
		t.Fatalf("%+v", out[0])
	}
}
