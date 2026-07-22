package protocol

import (
	"errors"
	"testing"
)

func TestFailfAndCodeOf(t *testing.T) {
	err := Failf(CodeCF403, "signup page status=%d", 403)
	if CodeOf(err) != CodeCF403 {
		t.Fatalf("code=%s", CodeOf(err))
	}
	if !stringsContains(err.Error(), "cf_403") {
		t.Fatalf("msg=%s", err.Error())
	}
	wrapped := Wrap(CodeGRPCCreate, "create", errors.New("boom"))
	if CodeOf(wrapped) != CodeGRPCCreate {
		t.Fatalf("wrap code=%s", CodeOf(wrapped))
	}
	// already Fail — preserve
	again := Wrap(CodeSignup, "x", err)
	if CodeOf(again) != CodeCF403 {
		t.Fatalf("preserve code=%s", CodeOf(again))
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
