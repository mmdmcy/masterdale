package dale

// Minimal OIDC relying-party support built only on the Go standard library.
// It replaces the previous github.com/coreos/go-oidc/v3 and
// golang.org/x/oauth2 dependencies with the small subset Masterdale uses:
// provider discovery, JWKS retrieval and caching, ID token verification
// (RS256/384/512, ES256/384/512, EdDSA), and the authorization code flow
// with S256 PKCE.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const oidcHTTPTimeout = 10 * time.Second

var oidcSupportedAlgs = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"ES256": true, "ES384": true, "ES512": true,
	"EdDSA": true,
}

type oidcDiscovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURL               string   `json:"jwks_uri"`
	SigningAlgs           []string `json:"id_token_signing_alg_values_supported"`
}

type oidcProvider struct {
	discovery oidcDiscovery
	keySet    *oidcKeySet
}

// oidcKeySet caches the provider's public keys and refetches them when a
// signature references an unknown key, bounded to one refetch per interval.
type oidcKeySet struct {
	mu      sync.Mutex
	jwksURL string
	keys    []jwksKey
	fetched time.Time
	refetch time.Time
}

const oidcKeySetRefetchInterval = 30 * time.Second

type jwksKey struct {
	kid string
	alg string
	key crypto.PublicKey
}

type oidcIDClaims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"`
	Expiry    int64    `json:"exp"`
	IssuedAt  int64    `json:"iat"`
	NotBefore int64    `json:"nbf"`
	Audience  audience `json:"aud"`
	Nonce     string   `json:"nonce"`
	Azp       string   `json:"azp"`
}

type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audience{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

type oidcTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	IDToken     string `json:"id_token"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func oidcHTTPClient() *http.Client {
	return &http.Client{Timeout: oidcHTTPTimeout}
}

func fetchOIDCDiscovery(ctx context.Context, issuer string) (*oidcProvider, error) {
	wellKnown, err := oidcWellKnownURL(issuer)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, err
	}
	resp, err := oidcHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery returned %s", resp.Status)
	}
	var discovery oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&discovery); err != nil {
		return nil, fmt.Errorf("OIDC discovery is not valid JSON: %w", err)
	}
	if discovery.Issuer != issuer {
		return nil, fmt.Errorf("OIDC discovery issuer %q does not match configured issuer %q", discovery.Issuer, issuer)
	}
	if err := validateOIDCURL("discovered authorization endpoint", discovery.AuthorizationEndpoint, false); err != nil {
		return nil, err
	}
	if err := validateOIDCURL("discovered token endpoint", discovery.TokenEndpoint, false); err != nil {
		return nil, err
	}
	if discovery.JWKSURL == "" {
		return nil, errors.New("OIDC discovery has no jwks_uri")
	}
	return &oidcProvider{discovery: discovery}, nil
}

// oidcWellKnownURL inserts /.well-known/openid-configuration between the
// host and any path component, per the OpenID Connect Discovery spec.
func oidcWellKnownURL(issuer string) (string, error) {
	parsed, err := url.Parse(issuer)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", errors.New("OIDC issuer must be an absolute URL")
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	wellKnown := "/.well-known/openid-configuration" + path
	rewritten := *parsed
	rewritten.Path = wellKnown
	rewritten.RawQuery = ""
	rewritten.Fragment = ""
	return rewritten.String(), nil
}

func (p *oidcProvider) allowedSigningAlgs() []string {
	var allowed []string
	for _, alg := range p.discovery.SigningAlgs {
		if oidcSupportedAlgs[alg] {
			allowed = append(allowed, alg)
		}
	}
	if len(allowed) == 0 {
		return []string{"RS256"}
	}
	return allowed
}

// verifyIDToken validates the compact JWS signature against the provider's
// JWKS and the issuer, audience, expiry, and issued-at claims. Nonce and
// subject allowlisting stay with the caller.
func (p *oidcProvider) verifyIDToken(rawToken, clientID string) (*oidcIDClaims, error) {
	parts := strings.Split(strings.TrimSpace(rawToken), ".")
	if len(parts) != 3 {
		return nil, errors.New("ID token is not a compact JWS")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("ID token header is not valid base64url: %w", err)
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("ID token header is not valid JSON: %w", err)
	}
	if !oidcSupportedAlgs[header.Alg] {
		return nil, fmt.Errorf("ID token uses unsupported algorithm %q", header.Alg)
	}
	allowed := false
	for _, alg := range p.allowedSigningAlgs() {
		if alg == header.Alg {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("ID token algorithm %q is not in the provider's advertised list", header.Alg)
	}
	signed := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("ID token signature is not valid base64url: %w", err)
	}
	key, err := p.keySet.selectKey(header.Kid, header.Alg)
	if err != nil {
		return nil, err
	}
	if err := verifyJWS(header.Alg, key, []byte(signed), signature); err != nil {
		return nil, fmt.Errorf("ID token signature verification failed: %w", err)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("ID token claims are not valid base64url: %w", err)
	}
	var claims oidcIDClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("ID token claims are not valid JSON: %w", err)
	}
	const leeway = 5 * time.Minute
	now := time.Now()
	if claims.Issuer != p.discovery.Issuer {
		return nil, fmt.Errorf("ID token issuer %q does not match %q", claims.Issuer, p.discovery.Issuer)
	}
	if claims.Subject == "" {
		return nil, errors.New("ID token has no subject")
	}
	if len(claims.Audience) == 0 {
		return nil, errors.New("ID token has no audience")
	}
	found := false
	for _, entry := range claims.Audience {
		if entry == clientID {
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("ID token audience does not include this client")
	}
	if claims.Azp != "" && claims.Azp != clientID {
		return nil, errors.New("ID token authorized party does not match this client")
	}
	if claims.Expiry == 0 || now.Unix() > claims.Expiry+int64(leeway.Seconds()) {
		return nil, errors.New("ID token has expired")
	}
	if claims.IssuedAt > 0 && claims.IssuedAt > now.Unix()+int64(leeway.Seconds()) {
		return nil, errors.New("ID token issued in the future")
	}
	if claims.NotBefore > 0 && now.Unix()+int64(leeway.Seconds()) < claims.NotBefore {
		return nil, errors.New("ID token not yet valid")
	}
	return &claims, nil
}

func verifyJWS(alg string, key crypto.PublicKey, signed, signature []byte) error {
	switch alg {
	case "RS256", "RS384", "RS512":
		public, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type does not match RSA algorithm")
		}
		var hash crypto.Hash
		switch alg {
		case "RS256":
			hash = crypto.SHA256
		case "RS384":
			hash = crypto.SHA384
		default:
			hash = crypto.SHA512
		}
		digest, err := hashBytes(hash, signed)
		if err != nil {
			return err
		}
		return rsa.VerifyPKCS1v15(public, hash, digest, signature)
	case "ES256", "ES384", "ES512":
		public, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("key type does not match ECDSA algorithm")
		}
		var hash crypto.Hash
		switch alg {
		case "ES256":
			hash = crypto.SHA256
		case "ES384":
			hash = crypto.SHA384
		default:
			hash = crypto.SHA512
		}
		digest, err := hashBytes(hash, signed)
		if err != nil {
			return err
		}
		r, s, err := splitECDSASignature(signature, public.Curve.Params().BitSize)
		if err != nil {
			return err
		}
		if !ecdsa.Verify(public, digest, r, s) {
			return errors.New("ECDSA signature does not match")
		}
		return nil
	case "EdDSA":
		public, ok := key.(ed25519.PublicKey)
		if !ok {
			return errors.New("key type does not match EdDSA algorithm")
		}
		if !ed25519.Verify(public, signed, signature) {
			return errors.New("Ed25519 signature does not match")
		}
		return nil
	default:
		return fmt.Errorf("unsupported algorithm %q", alg)
	}
}

func hashBytes(hash crypto.Hash, data []byte) ([]byte, error) {
	switch hash {
	case crypto.SHA256:
		sum := sha256.Sum256(data)
		return sum[:], nil
	case crypto.SHA384:
		sum := sha512.Sum384(data)
		return sum[:], nil
	case crypto.SHA512:
		sum := sha512.Sum512(data)
		return sum[:], nil
	default:
		return nil, fmt.Errorf("unsupported hash %v", hash)
	}
}

func splitECDSASignature(signature []byte, bitSize int) (*big.Int, *big.Int, error) {
	size := (bitSize + 7) / 8
	if len(signature) != 2*size {
		return nil, nil, errors.New("ECDSA signature has unexpected length")
	}
	return new(big.Int).SetBytes(signature[:size]), new(big.Int).SetBytes(signature[size:]), nil
}

func (p *oidcProvider) authCodeURL(config *OIDCConfig, state, nonce, challenge string) (string, error) {
	parsed, err := url.Parse(p.discovery.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	values := parsed.Query()
	values.Set("response_type", "code")
	values.Set("client_id", config.ClientID)
	values.Set("redirect_uri", config.RedirectURL)
	values.Set("scope", "openid profile")
	values.Set("state", state)
	values.Set("nonce", nonce)
	values.Set("code_challenge", challenge)
	values.Set("code_challenge_method", "S256")
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

// exchangeCode performs the token request with HTTP Basic client
// authentication, retrying with form-body credentials when the provider
// rejects client_secret_basic.
func (p *oidcProvider) exchangeCode(ctx context.Context, config *OIDCConfig, code, verifier string) (*oidcTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", config.RedirectURL)
	form.Set("code_verifier", verifier)

	basic := func() *http.Request {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
		req.SetBasicAuth(url.QueryEscape(config.ClientID), url.QueryEscape(config.ClientSecret))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}
	bodyForm := func() *http.Request {
		values := cloneURLValues(form)
		values.Set("client_id", config.ClientID)
		values.Set("client_secret", config.ClientSecret)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.discovery.TokenEndpoint, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}
	for _, build := range []func() *http.Request{basic, bodyForm} {
		token, retryable, err := doOIDCTokenRequest(build())
		if err != nil {
			return nil, err
		}
		if !retryable {
			return token, nil
		}
	}
	return nil, errors.New("token endpoint rejected both client authentication styles")
}

func doOIDCTokenRequest(req *http.Request) (*oidcTokenResponse, bool, error) {
	resp, err := oidcHTTPClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	var token oidcTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return nil, false, fmt.Errorf("token endpoint returned %s with an unreadable body", resp.Status)
	}
	if token.Error != "" {
		detail := token.Error
		if token.ErrorDesc != "" {
			detail += ": " + token.ErrorDesc
		}
		if (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) &&
			strings.EqualFold(token.Error, "invalid_client") {
			return nil, true, nil
		}
		return nil, false, errors.New(detail)
	}
	if resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("token endpoint returned %s", resp.Status)
	}
	if token.IDToken == "" {
		return nil, false, errors.New("token endpoint returned no ID token")
	}
	return &token, false, nil
}

func cloneURLValues(values url.Values) url.Values {
	cloned := url.Values{}
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func parseJWKS(data []byte) ([]jwksKey, error) {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("JWKS is not valid JSON: %w", err)
	}
	var keys []jwksKey
	for _, entry := range set.Keys {
		if entry.Use != "" && entry.Use != "sig" {
			continue
		}
		var key crypto.PublicKey
		switch entry.Kty {
		case "RSA":
			n, err := decodeBase64URLBigInt(entry.N)
			if err != nil {
				continue
			}
			e, err := decodeBase64URLBigInt(entry.E)
			if err != nil || !e.IsInt64() || e.Int64() < 3 || e.BitLen() > 32 {
				continue
			}
			key = &rsa.PublicKey{N: n, E: int(e.Int64())}
		case "EC":
			var curve elliptic.Curve
			switch entry.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			case "P-521":
				curve = elliptic.P521()
			default:
				continue
			}
			x, err := decodeBase64URLBigInt(entry.X)
			if err != nil {
				continue
			}
			y, err := decodeBase64URLBigInt(entry.Y)
			if err != nil {
				continue
			}
			key = &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		case "OKP":
			if entry.Crv != "Ed25519" {
				continue
			}
			x, err := decodeBase64URLBytes(entry.X)
			if err != nil || len(x) != ed25519.PublicKeySize {
				continue
			}
			key = ed25519.PublicKey(x)
		default:
			continue
		}
		keys = append(keys, jwksKey{kid: entry.Kid, alg: entry.Alg, key: key})
	}
	if len(keys) == 0 {
		return nil, errors.New("JWKS contains no usable signing keys")
	}
	return keys, nil
}

func decodeBase64URLBytes(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

func decodeBase64URLBigInt(value string) (*big.Int, error) {
	decoded, err := decodeBase64URLBytes(value)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(decoded), nil
}

func (k *oidcKeySet) selectKey(kid, alg string) (crypto.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil || !k.coversLocked(kid, alg) {
		known, wantRefetch := k.shouldRefetchLocked(kid, alg)
		if wantRefetch {
			if err := k.refreshLocked(); err != nil && !known {
				// No cached keys to fall back on.
				return nil, err
			}
		}
	}
	var matches []jwksKey
	for _, key := range k.keys {
		if kid != "" && key.kid != kid {
			continue
		}
		if key.alg != "" && key.alg != alg {
			continue
		}
		matches = append(matches, key)
	}
	if len(matches) == 1 {
		return matches[0].key, nil
	}
	if len(matches) > 1 {
		return nil, errors.New("multiple JWKS keys match the ID token key id")
	}
	if kid == "" && len(k.keys) == 1 {
		return k.keys[0].key, nil
	}
	return nil, fmt.Errorf("no JWKS key matches key id %q and algorithm %q", kid, alg)
}

// coversLocked reports whether the cached keys could plausibly serve a
// token signed with this key id and algorithm.
func (k *oidcKeySet) coversLocked(kid, alg string) bool {
	if time.Since(k.fetched) > time.Hour {
		return false
	}
	for _, key := range k.keys {
		if (kid == "" || key.kid == kid) && (key.alg == "" || key.alg == alg) {
			return true
		}
	}
	return false
}

// shouldRefetchLocked decides whether a cache miss may trigger a JWKS
// fetch (bounded to one refetch per interval so a flood of bad tokens
// cannot hammer the provider) and whether any cached keys exist.
func (k *oidcKeySet) shouldRefetchLocked(kid, alg string) (known bool, refetch bool) {
	if len(k.keys) > 0 {
		for _, key := range k.keys {
			if (kid == "" || key.kid == kid) && (key.alg == "" || key.alg == alg) {
				return true, time.Since(k.fetched) > time.Hour
			}
		}
		if time.Since(k.refetch) < oidcKeySetRefetchInterval {
			return true, false
		}
	}
	k.refetch = time.Now()
	return false, true
}

func (k *oidcKeySet) refreshLocked() error {
	req, err := http.NewRequest(http.MethodGet, k.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := oidcHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("fetching JWKS failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching JWKS returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	keys, err := parseJWKS(data)
	if err != nil {
		return err
	}
	k.keys = keys
	k.fetched = time.Now()
	return nil
}
