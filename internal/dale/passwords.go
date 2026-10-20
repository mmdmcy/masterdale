package dale

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// LinuxMice Passwords (LinuxMice ADR 0086): the browser extension's API.
//
// The extension pairs like a browser signs in: it shows an approval code,
// and `lmice signin CODE` on a device you own (or Allow on the Sign-in page)
// lets it in. It then holds a bearer token, which is a session in the same
// store, listed on the Sign-in page and ended there like any browser. Only
// that token works on these routes: no cookie (so no cross-site request can
// use a signed-in browser) and not the shared device token (so agents
// cannot read passwords through here).
//
//	POST /v1/passwords/pair      {"browser": "Brave on Linux"} → id, code, secret
//	GET  /v1/passwords/pair      ?id=&secret= → status, and the token once approved
//	GET  /v1/passwords/logins    ?site=HOST, or ?all=1: names, users, sites; never values
//	POST /v1/passwords/reveal    {"vault","name","field": password|user|code}
//	POST /v1/passwords/save      a new login, as the Vault tab's add
//	GET  /v1/passwords/generate  ?length=24
//	POST /v1/passwords/signout   ends this extension's session
//
// Values travel only in a reveal's answer and a save's body, exactly as on
// the Vault tab: never in a URL, a job, a log line or an error.

const (
	extensionHow           = "LinuxMice Passwords extension"
	passwordAlphabetLetter = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ"
	passwordAlphabetDigit  = "23456789"
	passwordAlphabetSymbol = "-_.!@#%+="
)

func isPasswordsRoute(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/v1/passwords/")
}

type passwordLogin struct {
	Vault   string   `json:"vault"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	User    string   `json:"user"`
	Domains []string `json:"domains"`
	HasTOTP bool     `json:"has_totp"`
}

func (s *Server) handlePasswords(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	action := strings.TrimPrefix(r.URL.Path, "/v1/passwords/")
	if action == "pair" {
		s.passwordsPair(w, r)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || s.sessions.extensionSubject(token) == "" {
		writeError(w, http.StatusUnauthorized, errors.New("connect LinuxMice Passwords again: this browser is not allowed (any more)"))
		return
	}
	if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("send JSON"))
		return
	}
	switch {
	case action == "logins" && r.Method == http.MethodGet:
		s.passwordsLogins(w, r)
	case action == "generate" && r.Method == http.MethodGet:
		length, _ := strconv.Atoi(r.URL.Query().Get("length"))
		value, err := generatePassword(length)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"value": value})
	case action == "reveal" && r.Method == http.MethodPost:
		s.passwordsReveal(w, r)
	case action == "save" && r.Method == http.MethodPost:
		s.passwordsSave(w, r)
	case action == "signout" && r.Method == http.MethodPost:
		s.sessions.endToken(token)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) passwordsPair(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Browser string `json:"browser"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		browser := cleanBrowserName(body.Browser)
		if browser == "" {
			browser = shortBrowser(r.UserAgent())
		}
		address := ""
		if ip := requestIP(r); ip != nil && !ip.IsLoopback() {
			address = ip.String()
		}
		wait, secret, err := s.sessions.newExtensionWait(browser, address)
		if err != nil {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": wait.ID, "code": wait.Code, "secret": secret,
			"expires_in": int(signinWaitTTL.Seconds()),
		})
	case http.MethodGet:
		q := r.URL.Query()
		status, name := s.sessions.collectKind(q.Get("id"), q.Get("secret"), true)
		if status != "approved" {
			writeJSON(w, http.StatusOK, map[string]string{"status": status})
			return
		}
		token, err := s.sessions.issueNamed(deviceSubject, extensionHow, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": status, "token": token})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// cleanBrowserName keeps a short, printable name the extension gives.
func cleanBrowserName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > 60 {
		return ""
	}
	for _, c := range name {
		if !unicode.IsPrint(c) {
			return ""
		}
	}
	return name
}

func (s *Server) vaultEntries(r *http.Request) ([]passwordLogin, error) {
	out, errOut, err := runLmice(r.Context(), []string{"vault", "--format", "json"}, nil, false, 30*time.Second)
	var report struct {
		Entries []passwordLogin `json:"entries"`
	}
	if len(out) == 0 || json.Unmarshal(out, &report) != nil {
		return nil, lmiceError(errOut, err)
	}
	return report.Entries, nil
}

func (s *Server) passwordsLogins(w http.ResponseWriter, r *http.Request) {
	entries, err := s.vaultEntries(r)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	site := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("site")))
	all := r.URL.Query().Get("all") == "1"
	logins := []passwordLogin{}
	for _, entry := range entries {
		if entry.Kind == "note" {
			continue
		}
		if entry.Domains == nil {
			entry.Domains = []string{}
		}
		if all || siteMatches(site, entry.Domains) {
			logins = append(logins, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": site, "logins": logins})
}

// siteMatches: the host is one of the domains or below one of them
// (accounts.github.com matches github.com); a leading www. is ignored.
func siteMatches(host string, domains []string) bool {
	host = strings.TrimPrefix(strings.TrimSuffix(host, "."), "www.")
	if host == "" {
		return false
	}
	for _, domain := range domains {
		domain = strings.TrimPrefix(strings.ToLower(domain), "www.")
		if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
			return true
		}
	}
	return false
}

func (s *Server) passwordsReveal(w http.ResponseWriter, r *http.Request) {
	if !LinuxMiceActionsEnabled() {
		writeError(w, http.StatusForbidden, errors.New("the vault is read-only on this node (DALE_LINUXMICE_ACTIONS)"))
		return
	}
	var ref vaultRef
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&ref); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request"))
		return
	}
	field := ref.Field
	ref.Field = ""
	spec, err := ref.spec()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch field {
	case "", "password":
	case "user":
		spec += ":user"
	case "code", "totp":
		// The current one-time code, never its seed.
		spec += ":code"
	default:
		writeError(w, http.StatusBadRequest, errors.New("fields are password, user and code"))
		return
	}
	out, errOut, err := runLmice(r.Context(), []string{"vault", "reveal", spec}, nil, false, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": strings.TrimRight(string(out), "\r\n")})
}

func (s *Server) passwordsSave(w http.ResponseWriter, r *http.Request) {
	if !LinuxMiceActionsEnabled() {
		writeError(w, http.StatusForbidden, errors.New("changes are turned off on this node (DALE_LINUXMICE_ACTIONS)"))
		return
	}
	var req vaultAddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request"))
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, errors.New("give a password"))
		return
	}
	args, payload, err := vaultAddArgs(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Saving never replaces: lmice add would, keeping the old one aside.
	entries, err := s.vaultEntries(r)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	for _, entry := range entries {
		if entry.Vault == req.Vault && entry.Name == req.Name {
			writeError(w, http.StatusConflict, errors.New(req.Vault+"/"+req.Name+" exists already; pick another name (change a login on the Vault page)"))
			return
		}
	}
	s.vaultChange(w, r, args, payload)
}

// generatePassword makes a password from letters, digits and a few
// symbols without look-alikes, with at least one of each.
func generatePassword(length int) (string, error) {
	if length == 0 {
		length = 24
	}
	if length < 12 {
		length = 12
	}
	if length > 128 {
		length = 128
	}
	alphabet := passwordAlphabetLetter + passwordAlphabetDigit + passwordAlphabetSymbol
	for {
		out := make([]byte, length)
		for i := range out {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			out[i] = alphabet[n.Int64()]
		}
		value := string(out)
		if strings.ContainsAny(value, "abcdefghijkmnpqrstuvwxyz") &&
			strings.ContainsAny(value, "ABCDEFGHJKLMNPQRSTUVWXYZ") &&
			strings.ContainsAny(value, passwordAlphabetDigit) &&
			strings.ContainsAny(value, passwordAlphabetSymbol) {
			return value, nil
		}
	}
}
