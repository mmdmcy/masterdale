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

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
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
	Issuer          string
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	AllowedSubjects map[string]struct{}
}

type oidcPendingFlow struct {
	browserTokenHash [32]byte
	nonce            string
	verifier         string
	expiresAt        time.Time
}

type oidcSession struct {
	subject   string
	expiresAt time.Time
}

type oidcRuntime struct {
	config   *OIDCConfig
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
	if err := validateOIDCURL("DALE_OIDC_REDIRECT_URL", values[3], true); err != nil {
		return nil, err
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
		RedirectURL: values[3], AllowedSubjects: allowed,
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
		config: config, flows: make(map[string]oidcPendingFlow),
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
	provider, oauthConfig, err := s.oidc.client(r.Context())
	if err != nil {
		http.Error(w, "LinuxMice sign-in is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	_ = provider
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
	http.Redirect(w, r, oauthConfig.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), http.StatusSeeOther)
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
	provider, oauthConfig, err := s.oidc.client(r.Context())
	if err != nil {
		http.Error(w, "LinuxMice sign-in is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	token, err := oauthConfig.Exchange(
		r.Context(), code, oauth2.SetAuthURLParam("code_verifier", flow.verifier),
	)
	if err != nil {
		http.Error(w, "LinuxMice sign-in failed", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "LinuxMice returned no identity", http.StatusBadGateway)
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: s.oidc.config.ClientID}).Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "LinuxMice identity verification failed", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Subject string `json:"sub"`
		Nonce   string `json:"nonce"`
	}
	if err := idToken.Claims(&claims); err != nil ||
		subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(flow.nonce)) != 1 {
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

func (o *oidcRuntime) client(ctx context.Context) (*oidc.Provider, oauth2.Config, error) {
	provider, err := oidc.NewProvider(ctx, o.config.Issuer)
	if err != nil {
		return nil, oauth2.Config{}, err
	}
	config := oauth2.Config{
		ClientID: o.config.ClientID, ClientSecret: o.config.ClientSecret,
		Endpoint: provider.Endpoint(), RedirectURL: o.config.RedirectURL,
		Scopes: []string{oidc.ScopeOpenID, "profile"},
	}
	if err := validateOIDCURL("discovered authorization endpoint", config.Endpoint.AuthURL, false); err != nil {
		return nil, oauth2.Config{}, err
	}
	if err := validateOIDCURL("discovered token endpoint", config.Endpoint.TokenURL, false); err != nil {
		return nil, oauth2.Config{}, err
	}
	return provider, config, nil
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
