package logx_test

import (
	"testing"

	"github.com/grok-free-register/grok-reg/internal/logx"
)

func TestKeepLine(t *testing.T) {
	lines := []struct {
		line string
		info bool
		dbg  bool
		warn bool
	}{
		{"[03:51:11] DBG [P2] Q ready", false, true, false},
		{"[03:51:08] INFO workers S=5", true, true, false},
		{"[03:51:31] ✓ 注册成功 #1", true, true, false},
		{"[03:51:31] → OAuth foo", true, true, false},
		{"[03:52:38] ! 探活失败", true, true, true},
		{"[03:52:38] ✗ boom", true, true, true},
	}
	for _, c := range lines {
		if got := logx.KeepLine(c.line, logx.LevelInfo); got != c.info {
			t.Errorf("info %q: got %v want %v", c.line, got, c.info)
		}
		if got := logx.KeepLine(c.line, logx.LevelDebug); got != c.dbg {
			t.Errorf("debug %q: got %v want %v", c.line, got, c.dbg)
		}
		if got := logx.KeepLine(c.line, logx.LevelWarn); got != c.warn {
			t.Errorf("warn %q: got %v want %v", c.line, got, c.warn)
		}
	}
}
