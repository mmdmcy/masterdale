package dale

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOIDCWellKnownURLInsertsPathPerSpec(t *testing.T) {
	cases := map[string]string{
		"https://identity.example.test":               "https://identity.example.test/.well-known/openid-configuration",
		"https://identity.example.test/realms/main":   "https://identity.example.test/.well-known/openid-configuration/realms/main",
		"https://identity.example.test/realms/main/":  "https://identity.example.test/.well-known/openid-configuration/realms/main",
		"https://identity.example.test/?issue=1#frag": "https://identity.example.test/.well-known/openid-configuration",
	}
	for issuer, want := range cases {
		got, err := oidcWellKnownURL(issuer)
		if err != nil {
			t.Fatalf("issuer %q: %v", issuer, err)
		}
		if got != want {
			t.Fatalf("issuer %q: got %q want %q", issuer, got, want)
		}
	}
}

func signJWS(t *testing.T, header map[string]any, claims map[string]any, sign func([]byte) []byte) string {
	t.Helper()
	mustJSON := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	signed := mustJSON(header) + "." + mustJSON(claims)
	return signed + "." + base64.RawURLEncoding.EncodeToString(sign([]byte(signed)))
}

func rsaTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func jwksForKeys(t *testing.T, keys ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func rsaJWK(t *testing.T, key *rsa.PrivateKey, kid string) map[string]any {
	t.Helper()
	return map[string]any{
		"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}
}

func idTokenClaims(issuer string, extra map[string]any) map[string]any {
	claims := map[string]any{
		"iss": issuer, "sub": "owner", "aud": "masterdale",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(),
	}
	for key, value := range extra {
		claims[key] = value
	}
	return claims
}

func TestOIDCDiscoveryTokenExchangeAndIDTokenVerification(t *testing.T) {
	key := rsaTestKey(t)
	issuer := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/auth",
				"token_endpoint":                        issuer + "/token",
				"jwks_uri":                              issuer + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256", "ES256"},
			})
		case "/jwks":
			w.Write(jwksForKeys(t, rsaJWK(t, key, "k1")))
		case "/token":
			if r.Header.Get("Authorization") != "" {
				// The provider only accepts client_secret_post; the client
				// must retry with credentials in the body.
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostFormValue("client_id") != "masterdale" ||
				r.PostFormValue("client_secret") != "secret" ||
				r.PostFormValue("code_verifier") != "the-verifier" ||
				r.PostFormValue("grant_type") != "authorization_code" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_request"})
				return
			}
			idToken := signJWS(t,
				map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"},
				idTokenClaims(issuer, map[string]any{"nonce": "n-123"}),
				func(data []byte) []byte {
					digest := sha256.Sum256(data)
					sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
					if err != nil {
						t.Fatal(err)
					}
					return sig
				})
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at", "token_type": "Bearer", "id_token": idToken,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL

	provider, err := fetchOIDCDiscovery(context.Background(), issuer)
	if err != nil {
		t.Fatal(err)
	}
	provider.keySet = &oidcKeySet{jwksURL: issuer + "/jwks"}
	config := &OIDCConfig{
		Issuer: issuer, ClientID: "masterdale", ClientSecret: "secret",
		RedirectURL: "https://masterdale.example.test/auth/oidc/callback",
	}
	authURL, err := provider.authCodeURL(config, "state-1", "n-123", "challenge-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authURL, "response_type=code") || !strings.Contains(authURL, "code_challenge_method=S256") || !strings.Contains(authURL, "nonce=n-123") {
		t.Fatalf("auth URL is missing expected parameters: %s", authURL)
	}
	token, err := provider.exchangeCode(context.Background(), config, "the-code", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := provider.verifyIDToken(token.IDToken, "masterdale")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "owner" || claims.Nonce != "n-123" {
		t.Fatalf("unexpected claims: subject=%q nonce=%q", claims.Subject, claims.Nonce)
	}
}

func TestOIDCTokenExchangeFailsWhenBothAuthStylesRejected(t *testing.T) {
	issuer := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
	}))
	defer server.Close()
	issuer = server.URL
	provider := &oidcProvider{discovery: oidcDiscovery{TokenEndpoint: issuer}}
	config := &OIDCConfig{ClientID: "masterdale", ClientSecret: "secret", RedirectURL: "https://cb.example.test/auth/oidc/callback"}
	if _, err := provider.exchangeCode(context.Background(), config, "code", "verifier"); err == nil {
		t.Fatal("expected both rejected auth styles to fail")
	}
}

func TestVerifyIDTokenRejectsTamperedExpiredOrForeignTokens(t *testing.T) {
	key := rsaTestKey(t)
	issuer := "https://identity.example.test"
	provider := &oidcProvider{
		discovery: oidcDiscovery{Issuer: issuer},
		keySet: &oidcKeySet{keys: []jwksKey{{
			kid: "k1", alg: "RS256", key: &key.PublicKey,
		}}},
	}
	sign := func(data []byte) []byte {
		digest := sha256.Sum256(data)
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	valid := signJWS(t, map[string]any{"alg": "RS256", "kid": "k1"}, idTokenClaims(issuer, nil), sign)
	if _, err := provider.verifyIDToken(valid, "masterdale"); err != nil {
		t.Fatalf("expected valid token to verify: %v", err)
	}

	if _, err := provider.verifyIDToken(valid+"x", "masterdale"); err == nil {
		t.Fatal("expected tampered signature to fail")
	}

	expired := signJWS(t, map[string]any{"alg": "RS256", "kid": "k1"}, idTokenClaims(issuer, map[string]any{
		"exp": time.Now().Add(-10 * time.Minute).Unix(),
	}), sign)
	if _, err := provider.verifyIDToken(expired, "masterdale"); err == nil {
		t.Fatal("expected expired token to fail")
	}

	foreign := signJWS(t, map[string]any{"alg": "RS256", "kid": "k1"}, idTokenClaims("https://other.example.test", nil), sign)
	if _, err := provider.verifyIDToken(foreign, "masterdale"); err == nil {
		t.Fatal("expected foreign issuer to fail")
	}

	wrongAud := signJWS(t, map[string]any{"alg": "RS256", "kid": "k1"}, idTokenClaims(issuer, map[string]any{
		"aud": "someone-else",
	}), sign)
	if _, err := provider.verifyIDToken(wrongAud, "masterdale"); err == nil {
		t.Fatal("expected wrong audience to fail")
	}

	multiAud := signJWS(t, map[string]any{"alg": "RS256", "kid": "k1"}, idTokenClaims(issuer, map[string]any{
		"aud": []string{"masterdale", "other"},
	}), sign)
	if _, err := provider.verifyIDToken(multiAud, "masterdale"); err != nil {
		t.Fatalf("expected multi-audience containing client to verify: %v", err)
	}

	multiAudBadAzp := signJWS(t, map[string]any{"alg": "RS256", "kid": "k1"}, idTokenClaims(issuer, map[string]any{
		"aud": []string{"masterdale", "other"}, "azp": "other",
	}), sign)
	if _, err := provider.verifyIDToken(multiAudBadAzp, "masterdale"); err == nil {
		t.Fatal("expected mismatched azp to fail")
	}

	future := signJWS(t, map[string]any{"alg": "RS256", "kid": "k1"}, idTokenClaims(issuer, map[string]any{
		"iat": time.Now().Add(30 * time.Minute).Unix(),
	}), sign)
	if _, err := provider.verifyIDToken(future, "masterdale"); err == nil {
		t.Fatal("expected future issued-at to fail")
	}

	unknownKid := signJWS(t, map[string]any{"alg": "RS256", "kid": "k2"}, idTokenClaims(issuer, nil), sign)
	if _, err := provider.verifyIDToken(unknownKid, "masterdale"); err == nil {
		t.Fatal("expected unknown key id to fail")
	}

	// Rate-limited refetch must not hammer the (absent) JWKS endpoint forever,
	// but the cached key must keep working for known kids.
	if _, err := provider.verifyIDToken(valid, "masterdale"); err != nil {
		t.Fatalf("expected cached key to keep verifying: %v", err)
	}
}

func TestVerifyIDTokenSupportsES256AndEdDSA(t *testing.T) {
	issuer := "https://identity.example.test"

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecProvider := &oidcProvider{
		discovery: oidcDiscovery{Issuer: issuer, SigningAlgs: []string{"ES256", "RS256"}},
		keySet: &oidcKeySet{keys: []jwksKey{{
			kid: "e1", alg: "ES256", key: &ecKey.PublicKey,
		}}},
	}
	ecToken := signJWS(t, map[string]any{"alg": "ES256", "kid": "e1"}, idTokenClaims(issuer, nil), func(data []byte) []byte {
		digest := sha256.Sum256(data)
		r, s, err := ecdsa.Sign(rand.Reader, ecKey, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		size := 32
		sig := make([]byte, 2*size)
		r.FillBytes(sig[:size])
		s.FillBytes(sig[size:])
		return sig
	})
	if _, err := ecProvider.verifyIDToken(ecToken, "masterdale"); err != nil {
		t.Fatalf("expected ES256 token to verify: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edProvider := &oidcProvider{
		discovery: oidcDiscovery{Issuer: issuer, SigningAlgs: []string{"EdDSA"}},
		keySet: &oidcKeySet{keys: []jwksKey{{
			kid: "o1", alg: "EdDSA", key: pub,
		}}},
	}
	edToken := signJWS(t, map[string]any{"alg": "EdDSA", "kid": "o1"}, idTokenClaims(issuer, nil), func(data []byte) []byte {
		return ed25519.Sign(priv, data)
	})
	if _, err := edProvider.verifyIDToken(edToken, "masterdale"); err != nil {
		t.Fatalf("expected EdDSA token to verify: %v", err)
	}
}

func TestParseJWKSHandlesRSAMixedAndSkipsUnsupportedKeys(t *testing.T) {
	rsaKey := rsaTestKey(t)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	jwks := jwksForKeys(t,
		rsaJWK(t, rsaKey, "r1"),
		map[string]any{"kty": "EC", "kid": "e1", "crv": "P-256", "use": "sig",
			"x": base64.RawURLEncoding.EncodeToString(ecKey.PublicKey.X.Bytes()),
			"y": base64.RawURLEncoding.EncodeToString(ecKey.PublicKey.Y.Bytes())},
		map[string]any{"kty": "OKP", "kid": "o1", "crv": "Ed25519",
			"x": base64.RawURLEncoding.EncodeToString(edPub)},
		map[string]any{"kty": "oct", "kid": "symmetric"},
		map[string]any{"kty": "EC", "kid": "weird-curve", "crv": "secp256k1",
			"x": "AA", "y": "AA"},
	)
	keys, err := parseJWKS(jwks)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 usable keys, got %d", len(keys))
	}
	if _, ok := keys[0].key.(*rsa.PublicKey); !ok {
		t.Fatal("expected first key to be RSA")
	}
	if _, ok := keys[1].key.(*ecdsa.PublicKey); !ok {
		t.Fatal("expected second key to be ECDSA")
	}
	if _, ok := keys[2].key.(ed25519.PublicKey); !ok {
		t.Fatal("expected third key to be Ed25519")
	}

	if _, err := parseJWKS([]byte("not-json")); err == nil {
		t.Fatal("expected invalid JWKS JSON to fail")
	}
	if _, err := parseJWKS([]byte(`{"keys":[]}`)); err == nil {
		t.Fatal("expected empty JWKS to fail")
	}
}

func TestAudienceUnmarshalAcceptsStringAndList(t *testing.T) {
	var single audience
	if err := json.Unmarshal([]byte(`"masterdale"`), &single); err != nil {
		t.Fatal(err)
	}
	if len(single) != 1 || single[0] != "masterdale" {
		t.Fatalf("unexpected single audience: %v", single)
	}
	var many audience
	if err := json.Unmarshal([]byte(`["a","b"]`), &many); err != nil {
		t.Fatal(err)
	}
	if len(many) != 2 {
		t.Fatalf("unexpected multi audience: %v", many)
	}
}
