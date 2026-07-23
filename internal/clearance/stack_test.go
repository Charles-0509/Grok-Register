package clearance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultPrivoxyImageEnvOverride(t *testing.T) {
	t.Setenv("PRIVOXY_IMAGE", "example.com/privoxy:custom")
	if got := DefaultPrivoxyImage(); got != "example.com/privoxy:custom" {
		t.Fatalf("got %s", got)
	}
}

func TestDefaultPrivoxyImageByArch(t *testing.T) {
	t.Setenv("PRIVOXY_IMAGE", "")
	got := DefaultPrivoxyImage()
	switch runtime.GOARCH {
	case "arm64", "arm":
		if got != privoxyImageARM64 {
			t.Fatalf("arch=%s image=%s want %s", runtime.GOARCH, got, privoxyImageARM64)
		}
	default:
		if got != privoxyImageAMD64 {
			t.Fatalf("arch=%s image=%s want %s", runtime.GOARCH, got, privoxyImageAMD64)
		}
	}
}

func TestEnsurePrivoxyImageEnvWritesDotEnv(t *testing.T) {
	t.Setenv("PRIVOXY_IMAGE", "")
	dir := t.TempDir()
	// seed unrelated key
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FOO=bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ensurePrivoxyImageEnv(dir)
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "FOO=bar") {
		t.Fatalf("lost FOO: %s", text)
	}
	if !strings.Contains(text, "PRIVOXY_IMAGE=") {
		t.Fatalf("missing PRIVOXY_IMAGE: %s", text)
	}
	// second write should not duplicate
	ensurePrivoxyImageEnv(dir)
	data2, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if c := strings.Count(string(data2), "PRIVOXY_IMAGE="); c != 1 {
		t.Fatalf("expected 1 PRIVOXY_IMAGE line, got %d in:\n%s", c, data2)
	}
}
