package dale

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	oidcFlowCookie    = "masterdale_oidc_flow"
	oidcSessionCookie = "masterdale_session"
	oidcFlowTTL       = 10 * time.Minute
	oidcSessionTTL    = 12 * time.Hour
	oidcMaxFlows      = 64
	oidcMaxSessions   = 64
)

type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is the first of DALE_OIDC_REDIRECT_URL's comma-separated
	// callbacks; RedirectURLs holds them all, one per address Masterdale is
	// reached at (for example a tailnet name and an internal name).
	RedirectURL     string
	RedirectURLs    []string
	AllowedSubjects map[string]struct{}
}

// redirectFor picks the callback on the address the browser used, so a
// sign-in started at one address comes back to it; the first otherwise.
func (c *OIDCConfig) redirectFor(host string) string {
	for _, candidate := range c.RedirectURLs {
		if parsed, err := url.Parse(candidate); err == nil && strings.EqualFold(parsed.Host, host) {
			return candidate
		}
	}
	return c.RedirectURL
}

type oidcPendingFlow struct {
	browserTokenHash [32]byte
	nonce            string
	verifier         string
	redirectURL      string
	expiresAt        time.Time
}

type oidcSession struct {
	subject   string
	expiresAt time.Time
}

type oidcRuntime struct {
	config   *OIDCConfig
	keySet   *oidcKeySet
	mu       sync.Mutex
	flows    map[string]oidcPendingFlow
	sessions map[[32]byte]oidcSession
}

func loadOIDCConfig() (*OIDCConfig, error) {
	values := []string{
		strings.TrimSpace(os.Getenv("DALE_OIDC_ISSUER")),
		strings.TrimSpace(os.Getenv("DALE_OIDC_CLIENT_ID")),
		strings.TrimSpace(os.Getenv("DALE_OIDC_CLIENT_SECRET")),
		strings.TrimSpace(os.Getenv("DALE_OIDC_REDIRECT_URL")),
		strings.TrimSpace(os.Getenv("DALE_OIDC_ALLOWED_SUBJECTS")),
	}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil, nil
	}
	if configured != len(values) {
		return nil, errors.New("DALE_OIDC_ISSUER, DALE_OIDC_CLIENT_ID, DALE_OIDC_CLIENT_SECRET, DALE_OIDC_REDIRECT_URL, and DALE_OIDC_ALLOWED_SUBJECTS must be set together")
	}
	if err := validateOIDCURL("DALE_OIDC_ISSUER", values[0], false); err != nil {
		return nil, err
	}
	// DALE_OIDC_EXTRA_REDIRECT_URLS adds callbacks outside the secret .env
	// (a systemd drop-in), e.g. Masterdale's internal name.
	var redirects []string
	for _, redirect := range strings.Split(values[3]+","+os.Getenv("DALE_OIDC_EXTRA_REDIRECT_URLS"), ",") {
		if redirect = strings.TrimSpace(redirect); redirect == "" {
			continue
		}
		if err := validateOIDCURL("DALE_OIDC_REDIRECT_URL", redirect, true); err != nil {
			return nil, err
		}
		redirects = append(redirects, redirect)
	}
	if len(redirects) == 0 {
		return nil, errors.New("DALE_OIDC_REDIRECT_URL must contain at least one callback")
	}
	allowed := make(map[string]struct{})
	for _, subject := range strings.Split(values[4], ",") {
		if subject = strings.TrimSpace(subject); subject != "" {
			allowed[subject] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("DALE_OIDC_ALLOWED_SUBJECTS must contain at least one subject")
	}
	return &OIDCConfig{
		Issuer: values[0], ClientID: values[1], ClientSecret: values[2],
		RedirectURL: redirects[0], RedirectURLs: redirects, AllowedSubjects: allowed,
	}, nil
}

func validateOIDCURL(name, raw string, callback bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", name)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s contains forbidden URL parts", name)
	}
	secure := parsed.Scheme == "https"
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		secure = strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	}
	if !secure {
		return fmt.Errorf("%s must use HTTPS; HTTP is allowed only for loopback testing", name)
	}
	if callback && parsed.Path != "/auth/oidc/callback" {
		return fmt.Errorf("%s must use the exact /auth/oidc/callback path", name)
	}
	return nil
}

func newOIDCRuntime(config *OIDCConfig) *oidcRuntime {
	if config == nil {
		return nil
	}
	return &oidcRuntime{
		config: config, keySet: &oidcKeySet{},
		flows:    make(map[string]oidcPendingFlow),
		sessions: make(map[[32]byte]oidcSession),
	}
}

func isOIDCRoute(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		(r.URL.Path == "/auth/oidc/start" || r.URL.Path == "/auth/oidc/callback")
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	provider, err := s.oidc.provider(r.Context())
	if err != nil {
		http.Error(w, "LinuxMice sign-in is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	state, err := randomURLToken(32)
	if err != nil {
		http.Error(w, "Could not start sign-in", http.StatusInternalServerError)
		return
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		http.Error(w, "Could not start sign-in", http.StatusInternalServerError)
		return
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		http.Error(w, "Could not start sign-in", http.StatusInternalServerError)
		return
	}
	browserToken, err := randomURLToken(32)
	if err != nil {
		http.Error(w, "Could not start sign-in", http.StatusInternalServerError)
		return
	}
	if !s.oidc.storeFlow(state, oidcPendingFlow{
		browserTokenHash: sha256.Sum256([]byte(browserToken)),
		nonce:            nonce, verifier: verifier, expiresAt: time.Now().Add(oidcFlowTTL),
		redirectURL: s.oidc.config.redirectFor(r.Host),
	}) {
		http.Error(w, "Too many pending sign-ins", http.StatusTooManyRequests)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcFlowCookie, Value: browserToken, Path: "/auth/oidc/callback",
		MaxAge: int(oidcFlowTTL.Seconds()), HttpOnly: true, Secure: s.oidc.secureCookie(),
		SameSite: http.SameSiteLaxMode,
	})
	challenge := base64.RawURLEncoding.EncodeToString(sha256Bytes(verifier))
	config := *s.oidc.config
	config.RedirectURL = config.redirectFor(r.Host)
	authURL, err := provider.authCodeURL(&config, state, nonce, challenge)
	if err != nil {
		http.Error(w, "LinuxMice sign-in is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("error") != "" {
		http.Error(w, "LinuxMice sign-in was not completed", http.StatusUnauthorized)
		return
	}
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	flowCookie, err := r.Cookie(oidcFlowCookie)
	if code == "" || state == "" || err != nil {
		http.Error(w, "Invalid LinuxMice sign-in response", http.StatusBadRequest)
		return
	}
	flow, ok := s.oidc.takeFlow(state, flowCookie.Value)
	if !ok {
		http.Error(w, "LinuxMice sign-in expired or failed browser verification", http.StatusBadRequest)
		return
	}
	provider, err := s.oidc.provider(r.Context())
	if err != nil {
		http.Error(w, "LinuxMice sign-in is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	config := *s.oidc.config
	if flow.redirectURL != "" {
		config.RedirectURL = flow.redirectURL
	}
	token, err := provider.exchangeCode(r.Context(), &config, code, flow.verifier)
	if err != nil {
		http.Error(w, "LinuxMice sign-in failed", http.StatusBadGateway)
		return
	}
	rawIDToken := token.IDToken
	if rawIDToken == "" {
		http.Error(w, "LinuxMice returned no identity", http.StatusBadGateway)
		return
	}
	claims, err := provider.verifyIDToken(rawIDToken, s.oidc.config.ClientID)
	if err != nil {
		http.Error(w, "LinuxMice identity verification failed", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(flow.nonce)) != 1 {
		http.Error(w, "LinuxMice identity verification failed", http.StatusUnauthorized)
		return
	}
	if _, allowed := s.oidc.config.AllowedSubjects[claims.Subject]; !allowed {
		http.Error(w, "This LinuxMice identity is not allowed", http.StatusForbidden)
		return
	}
	sessionToken, err := s.oidc.issueSession(claims.Subject)
	if err != nil {
		http.Error(w, "Could not create a session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcSessionCookie, Value: sessionToken, Path: "/",
		MaxAge: int(oidcSessionTTL.Seconds()), HttpOnly: true, Secure: s.oidc.secureCookie(),
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: oidcFlowCookie, Value: "", Path: "/auth/oidc/callback",
		MaxAge: -1, HttpOnly: true, Secure: s.oidc.secureCookie(), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// provider fetches the OIDC discovery document. The public key set is
// cached on the runtime so signatures can be verified across flows even
// when discovery is refetched.
func (o *oidcRuntime) provider(ctx context.Context) (*oidcProvider, error) {
	provider, err := fetchOIDCDiscovery(ctx, o.config.Issuer)
	if err != nil {
		return nil, err
	}
	o.keySet.mu.Lock()
	if o.keySet.jwksURL == "" {
		o.keySet.jwksURL = provider.discovery.JWKSURL
	}
	o.keySet.mu.Unlock()
	provider.keySet = o.keySet
	return provider, nil
}

func (o *oidcRuntime) storeFlow(state string, flow oidcPendingFlow) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	for key, candidate := range o.flows {
		if !candidate.expiresAt.After(now) {
			delete(o.flows, key)
		}
	}
	if len(o.flows) >= oidcMaxFlows {
		return false
	}
	o.flows[state] = flow
	return true
}

func (o *oidcRuntime) takeFlow(state, browserToken string) (oidcPendingFlow, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	flow, ok := o.flows[state]
	delete(o.flows, state)
	if !ok || !flow.expiresAt.After(time.Now()) {
		return oidcPendingFlow{}, false
	}
	got := sha256.Sum256([]byte(browserToken))
	if subtle.ConstantTimeCompare(got[:], flow.browserTokenHash[:]) != 1 {
		return oidcPendingFlow{}, false
	}
	return flow, true
}

func (o *oidcRuntime) issueSession(subject string) (string, error) {
	token, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	for key, session := range o.sessions {
		if !session.expiresAt.After(now) {
			delete(o.sessions, key)
		}
	}
	if len(o.sessions) >= oidcMaxSessions {
		return "", errors.New("too many active sessions")
	}
	o.sessions[sha256.Sum256([]byte(token))] = oidcSession{
		subject: subject, expiresAt: now.Add(oidcSessionTTL),
	}
	return token, nil
}

func (o *oidcRuntime) authorizeSession(r *http.Request) bool {
	cookie, err := r.Cookie(oidcSessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	key := sha256.Sum256([]byte(cookie.Value))
	session, ok := o.sessions[key]
	if !ok || !session.expiresAt.After(time.Now()) {
		delete(o.sessions, key)
		return false
	}
	_, allowed := o.config.AllowedSubjects[session.subject]
	return allowed
}

func (o *oidcRuntime) secureCookie() bool {
	return strings.HasPrefix(o.config.RedirectURL, "https://")
}

func randomURLToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
