package dale

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func clearOIDCEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"DALE_OIDC_ISSUER", "DALE_OIDC_CLIENT_ID", "DALE_OIDC_CLIENT_SECRET",
		"DALE_OIDC_REDIRECT_URL", "DALE_OIDC_ALLOWED_SUBJECTS",
	} {
		t.Setenv(name, "")
	}
}

func TestOIDCConfigIsOptionalAndRejectsPartialOrInsecureValues(t *testing.T) {
	clearOIDCEnv(t)
	config, err := loadOIDCConfig()
	if err != nil || config != nil {
		t.Fatalf("expected OIDC to be disabled: config=%#v err=%v", config, err)
	}

	t.Setenv("DALE_OIDC_ISSUER", "https://identity.example.test")
	if _, err := loadOIDCConfig(); err == nil {
		t.Fatal("expected partial OIDC configuration to fail")
	}

	t.Setenv("DALE_OIDC_CLIENT_ID", "masterdale")
	t.Setenv("DALE_OIDC_CLIENT_SECRET", "secret")
	t.Setenv("DALE_OIDC_REDIRECT_URL", "https://masterdale.example.test/auth/oidc/callback")
	t.Setenv("DALE_OIDC_ALLOWED_SUBJECTS", "owner")
	config, err = loadOIDCConfig()
	if err != nil || config == nil {
		t.Fatalf("expected valid OIDC config: config=%#v err=%v", config, err)
	}

	t.Setenv("DALE_OIDC_ISSUER", "http://identity.example.test")
	if _, err := loadOIDCConfig(); err == nil {
		t.Fatal("expected non-loopback HTTP issuer to fail")
	}
}

func TestOIDCSessionIsOpaqueBoundedAndAllowlisted(t *testing.T) {
	runtime := newOIDCRuntime(&OIDCConfig{
		RedirectURL: "https://masterdale.example.test/auth/oidc/callback",
		AllowedSubjects: map[string]struct{}{
			"owner": {},
		},
	})
	token, err := runtime.issueSession("owner")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/dashboard", nil)
	request.AddCookie(&http.Cookie{Name: oidcSessionCookie, Value: token})
	if !runtime.authorizeSession(request) {
		t.Fatal("expected issued owner session to authorize")
	}

	key := sha256Bytes(token)
	var hashed [32]byte
	copy(hashed[:], key)
	runtime.mu.Lock()
	session := runtime.sessions[hashed]
	session.expiresAt = time.Now().Add(-time.Second)
	runtime.sessions[hashed] = session
	runtime.mu.Unlock()
	if runtime.authorizeSession(request) {
		t.Fatal("expected expired session to fail")
	}
}

func TestForwardedLoopbackRequestIsNotDirectLoopback(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/dashboard", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	if !isDirectLoopbackRequest(request) {
		t.Fatal("expected an unforwarded loopback request to remain local")
	}
	request.Header.Set("X-Forwarded-Proto", "https")
	if isDirectLoopbackRequest(request) {
		t.Fatal("expected a reverse-proxied loopback request to require authentication")
	}
}

func TestLoopbackTrustRefusesWebPages(t *testing.T) {
	make := func(method, host, origin, contentType, site string) *http.Request {
		r := httptest.NewRequest(method, "http://"+host+"/v1/exec", nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		return r
	}
	trusted := []*http.Request{
		make(http.MethodPost, "127.0.0.1:7345", "", "application/json", ""),
		make(http.MethodGet, "localhost:7345", "", "", ""),
		make(http.MethodPost, "127.0.0.1:7345", "http://127.0.0.1:7345", "application/json", "same-origin"),
	}
	for _, r := range trusted {
		if !isTrustedLoopbackRequest(r) {
			t.Fatalf("should be trusted: %s %s origin=%q", r.Method, r.Host, r.Header.Get("Origin"))
		}
	}
	refused := []*http.Request{
		make(http.MethodPost, "127.0.0.1:7345", "https://evil.example", "application/json", ""),
		make(http.MethodPost, "127.0.0.1:7345", "", "text/plain", ""),
		make(http.MethodPost, "evil.example:7345", "", "application/json", ""),
		make(http.MethodGet, "127.0.0.1:7345", "", "", "cross-site"),
	}
	for _, r := range refused {
		if isTrustedLoopbackRequest(r) {
			t.Fatalf("should be refused: %s %s origin=%q type=%q", r.Method, r.Host, r.Header.Get("Origin"), r.Header.Get("Content-Type"))
		}
	}
}
