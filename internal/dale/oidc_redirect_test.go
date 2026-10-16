package dale

import "testing"

func TestSignInReturnsToTheAddressItStartedAt(t *testing.T) {
	t.Setenv("DALE_OIDC_ISSUER", "https://id.example.test")
	t.Setenv("DALE_OIDC_CLIENT_ID", "masterdale")
	t.Setenv("DALE_OIDC_CLIENT_SECRET", "secret")
	t.Setenv("DALE_OIDC_REDIRECT_URL", "https://a.example.test:7346/auth/oidc/callback")
	t.Setenv("DALE_OIDC_EXTRA_REDIRECT_URLS", " https://b.katteke.internal:7443/auth/oidc/callback")
	t.Setenv("DALE_OIDC_ALLOWED_SUBJECTS", "someone")
	config, err := loadOIDCConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.redirectFor("b.katteke.internal:7443"); got != "https://b.katteke.internal:7443/auth/oidc/callback" {
		t.Fatalf("katteke address: %s", got)
	}
	if got := config.redirectFor("elsewhere:1"); got != config.RedirectURL || got != "https://a.example.test:7346/auth/oidc/callback" {
		t.Fatalf("fallback: %s", got)
	}
	t.Setenv("DALE_OIDC_REDIRECT_URL", "https://a.example.test/auth/oidc/callback,http://evil.example.test/auth/oidc/callback")
	if _, err := loadOIDCConfig(); err == nil {
		t.Fatal("a plain-HTTP callback must be refused")
	}
}
