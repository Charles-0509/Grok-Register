package oauth

import "testing"

func TestDeviceAuthorized(t *testing.T) {
	if !deviceAuthorized("Device authorized successfully", "") {
		t.Fatal("body authorized")
	}
	if !deviceAuthorized("", "https://auth.x.ai/oauth2/device/done") {
		t.Fatal("loc done")
	}
	if deviceAuthorized("ok", "https://accounts.x.ai/oauth2/device/consent") {
		t.Fatal("consent is not authorized")
	}
}

func TestLooksLikeLogin(t *testing.T) {
	if looksLikeLogin("", "https://accounts.x.ai/sign-in/device?user_code=ABC") {
		t.Fatal("device page should not be login fail")
	}
	if looksLikeLogin("", "https://accounts.x.ai/oauth2/device/consent?user_code=ABC") {
		t.Fatal("consent should not be login fail")
	}
	if !looksLikeLogin("", "https://accounts.x.ai/sign-in") {
		t.Fatal("bare sign-in is login fail")
	}
	if !looksLikeLogin(`<input type="password" name="password">`, "") {
		t.Fatal("password form is login fail")
	}
}

func TestShortSSO(t *testing.T) {
	s := shortSSO("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signatureextra")
	if s == "" || len(s) >= len("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signatureextra") {
		t.Fatalf("shortSSO=%q", s)
	}
}
