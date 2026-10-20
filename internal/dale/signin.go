package dale

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Signing in from a device you own. A browser is signed in when someone
// who can already reach this device approves it, without a password:
//
//   - a one-time link: `lmice signin` on any device asks this one (over SSH,
//     a local request) for a link that opens signed in;
//   - an approval code: the sign-in page shows a code, and `lmice signin
//     CODE` or a signed-in browser (LinuxMice → Sign-in) allows it. The
//     waiting browser alone collects the session (a cookie binds it), so
//     knowing the code is not enough to take it.
//
// The password sign-in (lmidentityd, OIDC) stays as the other way in.
// Sessions last 30 days and survive a restart: only their SHA-256 hashes
// are written, to DataDir/sessions.json.

const (
	sessionCookie    = "masterdale_session"
	signinWaitCookie = "masterdale_signin_wait"
	sessionTTL       = 30 * 24 * time.Hour
	signinLinkTTL    = 5 * time.Minute
	signinWaitTTL    = 10 * time.Minute
	maxSessions      = 64
	maxSigninWaits   = 16
	maxSigninLinks   = 16
	// DeviceSubject marks a session a device approved (not a password).
	deviceSubject = "device"
	// Codes avoid look-alike characters (no 0/O, 1/I/L).
	codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

type storedSession struct {
	Subject   string    `json:"subject"`
	How       string    `json:"how"`
	Browser   string    `json:"browser"`
	Created   time.Time `json:"created"`
	ExpiresAt time.Time `json:"expires_at"`
}

type signinWait struct {
	ID         string
	Code       string
	secretHash [32]byte
	Browser    string
	Address    string
	Created    time.Time
	ExpiresAt  time.Time
	Approved   bool
	// Extension: LinuxMice Passwords waits for this approval, not a
	// browser page; it collects a bearer token instead of a cookie.
	Extension bool
}

type sessionStore struct {
	mu       sync.Mutex
	path     string
	sessions map[string]storedSession // hex SHA-256 of the cookie value
	links    map[[32]byte]time.Time
	waits    map[string]*signinWait // by ID
}

func newSessionStore(dataDir string) *sessionStore {
	store := &sessionStore{
		sessions: map[string]storedSession{},
		links:    map[[32]byte]time.Time{},
		waits:    map[string]*signinWait{},
	}
	if dataDir != "" {
		store.path = filepath.Join(dataDir, "sessions.json")
		if data, err := os.ReadFile(store.path); err == nil {
			_ = json.Unmarshal(data, &store.sessions)
		}
	}
	return store
}

func hashHex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// save writes the session hashes; the caller holds mu.
func (st *sessionStore) save() {
	if st.path == "" {
		return
	}
	data, err := json.Marshal(st.sessions)
	if err != nil {
		return
	}
	temp := st.path + ".tmp"
	if os.WriteFile(temp, data, 0o600) == nil {
		_ = os.Rename(temp, st.path)
	}
}

// prune drops what expired; the caller holds mu.
func (st *sessionStore) prune(now time.Time) {
	for key, session := range st.sessions {
		if !session.ExpiresAt.After(now) {
			delete(st.sessions, key)
		}
	}
	for key, expires := range st.links {
		if !expires.After(now) {
			delete(st.links, key)
		}
	}
	for id, wait := range st.waits {
		if !wait.ExpiresAt.After(now) {
			delete(st.waits, id)
		}
	}
}

func (st *sessionStore) issue(subject, how, browser string) (string, error) {
	return st.issueNamed(subject, how, shortBrowser(browser))
}

// issueNamed is issue with the browser already named ("LinuxMice
// Passwords in Brave on Linux").
func (st *sessionStore) issueNamed(subject, how, name string) (string, error) {
	token, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	st.prune(now)
	if len(st.sessions) >= maxSessions {
		// The oldest goes, so a new sign-in always works.
		oldest := ""
		for key, session := range st.sessions {
			if oldest == "" || session.Created.Before(st.sessions[oldest].Created) {
				oldest = key
			}
		}
		delete(st.sessions, oldest)
	}
	st.sessions[hashHex(token)] = storedSession{
		Subject: subject, How: how, Browser: name,
		Created: now.UTC(), ExpiresAt: now.Add(sessionTTL).UTC(),
	}
	st.save()
	return token, nil
}

// subject is the signed-in subject of the request's session, or "".
// An extension's token is not a browser session: as a cookie it signs
// nothing in.
func (st *sessionStore) subject(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return ""
	}
	session, ok := st.lookup(cookie.Value)
	if !ok || session.How == extensionHow {
		return ""
	}
	return session.Subject
}

// extensionSubject is the subject of a LinuxMice Passwords bearer token,
// or "" (a browser's cookie value is not accepted here).
func (st *sessionStore) extensionSubject(token string) string {
	if token == "" {
		return ""
	}
	session, ok := st.lookup(token)
	if !ok || session.How != extensionHow {
		return ""
	}
	return session.Subject
}

// endToken signs out the session a token belongs to.
func (st *sessionStore) endToken(token string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, hashHex(token))
	st.save()
}

func (st *sessionStore) lookup(token string) (storedSession, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := hashHex(token)
	session, ok := st.sessions[key]
	if !ok {
		return storedSession{}, false
	}
	if !session.ExpiresAt.After(time.Now()) {
		delete(st.sessions, key)
		st.save()
		return storedSession{}, false
	}
	return session, true
}

func (st *sessionStore) end(r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, hashHex(cookie.Value))
	st.save()
}

func (st *sessionStore) newLink() (string, error) {
	code, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.prune(time.Now())
	if len(st.links) >= maxSigninLinks {
		return "", errors.New("too many sign-in links waiting; wait five minutes")
	}
	st.links[sha256.Sum256([]byte(code))] = time.Now().Add(signinLinkTTL)
	return code, nil
}

func (st *sessionStore) takeLink(code string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := sha256.Sum256([]byte(code))
	expires, ok := st.links[key]
	delete(st.links, key)
	return ok && expires.After(time.Now())
}

func newApprovalCode() (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	code := make([]byte, 0, 9)
	for i, b := range buffer {
		if i == 4 {
			code = append(code, '-')
		}
		code = append(code, codeAlphabet[int(b)%len(codeAlphabet)])
	}
	return string(code), nil
}

// normalCode accepts "k7qm x2pa", "K7QMX2PA" and "K7QM-X2PA".
func normalCode(code string) string {
	var out []byte
	for _, c := range strings.ToUpper(code) {
		if strings.ContainsRune(codeAlphabet, c) {
			out = append(out, byte(c))
		}
	}
	if len(out) != 8 {
		return ""
	}
	return string(out[:4]) + "-" + string(out[4:])
}

func (st *sessionStore) newWait(browser, address string) (*signinWait, string, error) {
	return st.startWait(shortBrowser(browser), address, false)
}

// newExtensionWait is a wait for LinuxMice Passwords: the same code, allowed
// the same way (lmice signin CODE, Allow on the Sign-in page).
func (st *sessionStore) newExtensionWait(browser, address string) (*signinWait, string, error) {
	return st.startWait("LinuxMice Passwords in "+browser, address, true)
}

func (st *sessionStore) startWait(name, address string, extension bool) (*signinWait, string, error) {
	id, err := randomURLToken(24)
	if err != nil {
		return nil, "", err
	}
	secret, err := randomURLToken(32)
	if err != nil {
		return nil, "", err
	}
	code, err := newApprovalCode()
	if err != nil {
		return nil, "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	st.prune(now)
	if len(st.waits) >= maxSigninWaits {
		return nil, "", errors.New("too many browsers are waiting to sign in; wait ten minutes")
	}
	wait := &signinWait{
		ID: id, Code: code, secretHash: sha256.Sum256([]byte(secret)),
		Browser: name, Address: address,
		Created: now, ExpiresAt: now.Add(signinWaitTTL), Extension: extension,
	}
	st.waits[id] = wait
	copied := *wait
	return &copied, secret, nil
}

// approve allows the waiting browser that shows code.
func (st *sessionStore) approve(code string) (signinWait, bool) {
	code = normalCode(code)
	if code == "" {
		return signinWait{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.prune(time.Now())
	for _, wait := range st.waits {
		if subtle.ConstantTimeCompare([]byte(wait.Code), []byte(code)) == 1 {
			wait.Approved = true
			return *wait, true
		}
	}
	return signinWait{}, false
}

// collect returns "pending", "approved" (and removes the wait) or "gone".
func (st *sessionStore) collect(id, secret string) string {
	status, _ := st.collectKind(id, secret, false)
	return status
}

// collectKind is collect for a browser page (extension false) or for
// LinuxMice Passwords (true); a wait only answers its own kind. It also
// returns the waiting browser's name.
func (st *sessionStore) collectKind(id, secret string, extension bool) (string, string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	wait, ok := st.waits[id]
	if !ok || !wait.ExpiresAt.After(time.Now()) {
		delete(st.waits, id)
		return "gone", ""
	}
	got := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(got[:], wait.secretHash[:]) != 1 || wait.Extension != extension {
		return "gone", ""
	}
	if !wait.Approved {
		return "pending", ""
	}
	delete(st.waits, id)
	return "approved", wait.Browser
}

type waitingBrowser struct {
	Code    string `json:"code"`
	Browser string `json:"browser"`
	Address string `json:"address"`
	Since   string `json:"since"`
}

func (st *sessionStore) waiting() []waitingBrowser {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.prune(time.Now())
	out := []waitingBrowser{}
	for _, wait := range st.waits {
		if !wait.Approved {
			out = append(out, waitingBrowser{
				Code: wait.Code, Browser: wait.Browser, Address: wait.Address,
				Since: wait.Created.UTC().Format(time.RFC3339),
			})
		}
	}
	return out
}

type signedInBrowser struct {
	How     string `json:"how"`
	Browser string `json:"browser"`
	Since   string `json:"since"`
	Until   string `json:"until"`
	This    bool   `json:"this"`
}

func (st *sessionStore) list(r *http.Request) []signedInBrowser {
	this := ""
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		this = hashHex(cookie.Value)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.prune(time.Now())
	out := []signedInBrowser{}
	for key, session := range st.sessions {
		out = append(out, signedInBrowser{
			How: session.How, Browser: session.Browser,
			Since: session.Created.Format(time.RFC3339), Until: session.ExpiresAt.Format(time.RFC3339),
			This: key == this,
		})
	}
	return out
}

// endOthers signs every browser out except the one asking.
func (st *sessionStore) endOthers(r *http.Request) int {
	this := ""
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		this = hashHex(cookie.Value)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	ended := 0
	for key := range st.sessions {
		if key != this {
			delete(st.sessions, key)
			ended++
		}
	}
	st.save()
	return ended
}

// shortBrowser names a browser from its User-Agent ("Chrome on Mac").
func shortBrowser(agent string) string {
	name := "A browser"
	for _, candidate := range []struct{ token, name string }{
		{"Edg/", "Edge"}, {"Brave", "Brave"}, {"Firefox/", "Firefox"},
		{"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(agent, candidate.token) {
			name = candidate.name
			break
		}
	}
	system := ""
	for _, candidate := range []struct{ token, name string }{
		{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"},
		{"Mac OS X", "Mac"}, {"Windows", "Windows"}, {"Linux", "Linux"},
	} {
		if strings.Contains(agent, candidate.token) {
			system = candidate.name
			break
		}
	}
	if system == "" {
		return name
	}
	return name + " on " + system
}

// signedIn is a person's browser session (password or device-approved),
// not the device token and not a local request.
func (s *Server) signedIn(r *http.Request) bool {
	subject := s.sessions.subject(r)
	if subject == "" {
		return false
	}
	if subject == deviceSubject {
		return true
	}
	if s.oidc == nil {
		return false
	}
	_, allowed := s.oidc.config.AllowedSubjects[subject]
	return allowed
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: s.secureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// secureRequest: the browser reached us over HTTPS (Tailscale Serve or
// nginx in front), so cookies get Secure.
func (s *Server) secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
		(s.oidc != nil && s.oidc.secureCookie())
}

// publicURLs are the addresses browsers reach this GUI at: DALE_PUBLIC_URL
// (comma-separated), else the origins of the sign-in callbacks.
func (s *Server) publicURLs() []string {
	var out []string
	add := func(raw string) {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return
		}
		origin := parsed.Scheme + "://" + parsed.Host
		for _, have := range out {
			if have == origin {
				return
			}
		}
		out = append(out, origin)
	}
	for _, raw := range strings.Split(os.Getenv("DALE_PUBLIC_URL"), ",") {
		add(raw)
	}
	if s.oidc != nil {
		for _, raw := range s.oidc.config.RedirectURLs {
			add(raw)
		}
	}
	return out
}

func isSigninRoute(r *http.Request) bool {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && (path == "/signin" || strings.HasPrefix(path, "/auth/link/")):
		return true
	case path == "/auth/wait" && (r.Method == http.MethodPost || r.Method == http.MethodGet):
		return true
	case path == "/auth/signout" && r.Method == http.MethodPost:
		return true
	}
	return false
}

func (s *Server) handleSigninPage(w http.ResponseWriter, r *http.Request) {
	if s.signedIn(r) && r.URL.Query().Get("stay") == "" {
		http.Redirect(w, r, "/fleet", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = w.Write([]byte(signinHTML))
}

// GET /auth/link/CODE: a one-time link from `lmice signin`.
func (s *Server) handleSigninLink(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimPrefix(r.URL.Path, "/auth/link/")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if code == "" || !s.sessions.takeLink(code) {
		http.Redirect(w, r, "/signin?link=used", http.StatusSeeOther)
		return
	}
	token, err := s.sessions.issue(deviceSubject, "link from a device", r.UserAgent())
	if err != nil {
		http.Error(w, "Could not sign in", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, r, token)
	http.Redirect(w, r, "/fleet", http.StatusSeeOther)
}

// POST /auth/wait starts waiting for approval; GET /auth/wait?id= collects.
func (s *Server) handleSigninWait(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost {
		if !sameOriginJSON(r) {
			writeError(w, http.StatusForbidden, errors.New("needs a same-origin JSON request"))
			return
		}
		address := ""
		if ip := requestIP(r); ip != nil && !ip.IsLoopback() {
			address = ip.String()
		}
		wait, secret, err := s.sessions.newWait(r.UserAgent(), address)
		if err != nil {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: signinWaitCookie, Value: secret, Path: "/auth/wait",
			MaxAge: int(signinWaitTTL.Seconds()), HttpOnly: true, Secure: s.secureRequest(r),
			SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"id": wait.ID, "code": wait.Code,
			"expires_in": int(signinWaitTTL.Seconds()),
			"password":   s.oidc != nil,
		})
		return
	}
	cookie, err := r.Cookie(signinWaitCookie)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "gone"})
		return
	}
	status := s.sessions.collect(r.URL.Query().Get("id"), cookie.Value)
	if status == "approved" {
		token, err := s.sessions.issue(deviceSubject, "approved from a device", r.UserAgent())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.setSessionCookie(w, r, token)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (s *Server) handleSignout(w http.ResponseWriter, r *http.Request) {
	if !sameOriginJSON(r) {
		writeError(w, http.StatusForbidden, errors.New("needs a same-origin JSON request"))
		return
	}
	s.sessions.end(r)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// /v1/session/...: behind authMiddleware.
//
//	GET  /v1/session            this browser, the signed-in browsers and the waiting ones
//	POST /v1/session/link       a one-time sign-in link (a local request or a signed-in browser)
//	POST /v1/session/approve    {"code": "K7QM-X2PA"} allows a waiting browser
//	POST /v1/session/end-others signs every other browser out
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/session"), "/")
	// Who may approve or make links: someone on this device (lmice over
	// SSH, a local request) or a person already signed in. Not the shared
	// device token.
	trusted := isTrustedLoopbackRequest(r) || s.signedIn(r)
	if action == "" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"signed_in": s.signedIn(r),
			"browsers":  s.sessions.list(r),
			"waiting":   s.sessions.waiting(),
			"addresses": s.publicURLs(),
			"password":  s.oidc != nil,
		})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginJSON(r) {
		writeError(w, http.StatusForbidden, errors.New("needs a same-origin JSON request"))
		return
	}
	if !trusted {
		writeError(w, http.StatusForbidden, errors.New("sign-in links and approvals come from a device you own (lmice signin) or a signed-in browser"))
		return
	}
	switch action {
	case "link":
		code, err := s.sessions.newLink()
		if err != nil {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		var links []string
		for _, base := range s.publicURLs() {
			links = append(links, base+"/auth/link/"+code)
		}
		if len(links) == 0 {
			scheme := "http"
			if s.secureRequest(r) {
				scheme = "https"
			}
			links = append(links, scheme+"://"+r.Host+"/auth/link/"+code)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"url": links[0], "urls": links, "expires_in": int(signinLinkTTL.Seconds()),
		})
	case "approve":
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		wait, ok := s.sessions.approve(body.Code)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("no browser is waiting with that code; codes last ten minutes"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"approved": true, "code": wait.Code, "browser": wait.Browser, "address": wait.Address,
		})
	case "end-others":
		// From a browser: every other one; from lmice: all of them.
		writeJSON(w, http.StatusOK, map[string]int{"signed_out": s.sessions.endOthers(r)})
	default:
		http.NotFound(w, r)
	}
}
