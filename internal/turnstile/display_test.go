package turnstile

import (
	"runtime"
	"strings"
	"testing"
)

func TestNeedsVirtualDisplay(t *testing.T) {
	if !needsVirtualDisplay("offscreen") {
		t.Fatal("offscreen should need display")
	}
	if !needsVirtualDisplay("auto") {
		t.Fatal("auto should need display")
	}
	if !needsVirtualDisplay("") {
		t.Fatal("empty should need display")
	}
	if needsVirtualDisplay("headless") {
		t.Fatal("headless should not need display")
	}
}

func TestWrapForDisplayHeadlessNoWrap(t *testing.T) {
	bin, args, err := wrapForDisplay("headless", "/usr/bin/python3", []string{"script.py"})
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/usr/bin/python3" {
		t.Fatalf("bin=%s", bin)
	}
	if len(args) != 1 || args[0] != "script.py" {
		t.Fatalf("args=%v", args)
	}
}

func TestWrapForDisplayWithDISPLAY(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	bin, args, err := wrapForDisplay("offscreen", "/usr/bin/python3", []string{"script.py", "--mode", "offscreen"})
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/usr/bin/python3" {
		t.Fatalf("bin=%s want python (DISPLAY set)", bin)
	}
	if len(args) < 1 || args[0] != "script.py" {
		t.Fatalf("args=%v", args)
	}
}

func TestWrapForDisplayNoDisplayLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("xvfb wrap only on linux")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	// reset cache of xvfb path for test isolation is hard (sync.Once);
	// just assert error message shape when xvfb missing, or wrap when present.
	bin, args, err := wrapForDisplay("offscreen", "/usr/bin/python3", []string{"script.py"})
	if err != nil {
		if !strings.Contains(err.Error(), "xvfb") && !strings.Contains(err.Error(), "DISPLAY") {
			t.Fatalf("error should mention xvfb/DISPLAY: %v", err)
		}
		return
	}
	if !strings.Contains(bin, "xvfb") {
		t.Fatalf("expected xvfb-run wrap, bin=%s args=%v", bin, args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/usr/bin/python3") || !strings.Contains(joined, "script.py") {
		t.Fatalf("wrapped args missing python/script: %v", args)
	}
}

func TestDisplayHint(t *testing.T) {
	if DisplayHint("headless") != "true-headless" {
		t.Fatalf("hint headless=%s", DisplayHint("headless"))
	}
	t.Setenv("DISPLAY", ":99")
	t.Setenv("WAYLAND_DISPLAY", "")
	if h := DisplayHint("offscreen"); h != "DISPLAY=:99" {
		t.Fatalf("hint=%s", h)
	}
}
