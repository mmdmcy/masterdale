package dale

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The Vault tab (LinuxMice ADR 0064): every action is one `lmice vault`
// command. Values only travel as a request body into lmice's stdin, or as
// the body of a copy request back to the signed-in browser; they are never
// in a job, a URL, a log line or an error.

var (
	vaultNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	vaultSecretPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	vaultSitePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}\.[A-Za-z]{2,}$`)
	vaultHexPattern    = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
)

type vaultAddRequest struct {
	Vault    string   `json:"vault"`
	Name     string   `json:"name"`
	User     string   `json:"user"`
	Domains  []string `json:"domains"`
	Password string   `json:"password"`
	TOTP     string   `json:"totp"`
	Note     string   `json:"note"`
}

type vaultRef struct {
	Vault string `json:"vault"`
	Name  string `json:"name"`
	Field string `json:"field"`
}

func (ref vaultRef) spec() (string, error) {
	if !vaultNamePattern.MatchString(ref.Vault) || !vaultSecretPattern.MatchString(ref.Name) {
		return "", errors.New("pick a vault and a secret")
	}
	spec := ref.Vault + "/" + ref.Name
	switch ref.Field {
	case "", "password":
	case "totp", "note":
		spec += ":" + ref.Field
	default:
		return "", errors.New("fields are password, totp and note")
	}
	return spec, nil
}

// runLmice runs one lmice command. Changes go through the user's systemd
// manager when dale itself runs sandboxed (ProtectHome=read-only): lmice
// must write its vault, state and change log in the home folder.
func runLmice(ctx context.Context, args []string, stdin []byte, changes bool, timeout time.Duration) (stdout, stderr []byte, err error) {
	binary := LinuxMiceBinary()
	if binary == "" {
		return nil, nil, errors.New("lmice is not installed on this node")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := lmiceCommand(cctx, binary, args, changes)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// lmiceCommand builds the command; see runLmice.
func lmiceCommand(ctx context.Context, binary string, args []string, changes bool) *exec.Cmd {
	if changes && sandboxedService() {
		uid := os.Getuid()
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, ".local", "bin") + ":/usr/local/bin:/usr/bin:/bin"
		runner := []string{
			"--user", "--wait", "--pipe", "--quiet", "--collect",
			"--setenv=LMICE_VIA=masterdale",
			"--setenv=HOME=" + home,
			"--setenv=PATH=" + path,
			"--", binary,
		}
		cmd := exec.CommandContext(ctx, "systemd-run", append(runner, args...)...)
		cmd.Env = append(os.Environ(), fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid))
		return cmd
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	// lmice's change log records who started a change.
	cmd.Env = append(os.Environ(), "LMICE_VIA=masterdale")
	return cmd
}

// sandboxedService reports whether dale runs as a systemd service whose
// home folder is read-only; DALE_LINUXMICE_RUNNER=direct turns the user
// manager route off.
func sandboxedService() bool {
	if os.Getenv("INVOCATION_ID") == "" || strings.EqualFold(os.Getenv("DALE_LINUXMICE_RUNNER"), "direct") {
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

func lmiceError(stderr []byte, err error) error {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "audit:") && !strings.Contains(line, "approval flag accepted") {
			return errors.New(strings.TrimPrefix(line, "error: "))
		}
	}
	if err != nil {
		return err
	}
	return errors.New("lmice failed")
}

func (s *Server) handleVault(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/v1/vault")
	action = strings.TrimPrefix(action, "/")
	if action == "" && r.Method == http.MethodGet {
		out, errOut, err := runLmice(r.Context(), []string{"vault", "--format", "json"}, nil, false, 30*time.Second)
		if !json.Valid(out) || len(out) == 0 {
			writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(out)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if action == "answer" {
		s.handleVaultAnswer(w, r)
		return
	}
	if !sameOriginJSON(r) {
		writeError(w, http.StatusForbidden, errors.New("vault actions need a same-origin JSON request"))
		return
	}
	if !LinuxMiceActionsEnabled() {
		writeError(w, http.StatusForbidden, errors.New("changes are turned off on this node (DALE_LINUXMICE_ACTIONS)"))
		return
	}
	body := http.MaxBytesReader(w, r.Body, 64<<10)
	switch action {
	case "add":
		var req vaultAddRequest
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		spec, err := vaultRef{Vault: req.Vault, Name: req.Name}.spec()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Password == "" && req.Note == "" {
			writeError(w, http.StatusBadRequest, errors.New("give a password or a note"))
			return
		}
		args := []string{"--yes", "vault", "add", spec, "--json"}
		if user := strings.TrimSpace(req.User); user != "" {
			if len(user) > 200 || strings.ContainsAny(user, "\r\n\x00") {
				writeError(w, http.StatusBadRequest, errors.New("the user name is one line"))
				return
			}
			args = append(args, "--user", user)
		}
		for _, domain := range req.Domains {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if domain == "" {
				continue
			}
			if !vaultSitePattern.MatchString(domain) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a site (example: github.com)", domain))
				return
			}
			args = append(args, "--domain", domain)
		}
		payload, _ := json.Marshal(map[string]string{"password": req.Password, "totp": req.TOTP, "note": req.Note})
		s.vaultChange(w, r, args, payload)
	case "remove", "rotate", "push":
		var ref vaultRef
		if err := json.NewDecoder(body).Decode(&ref); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		ref.Field = ""
		spec, err := ref.spec()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.vaultChange(w, r, []string{"--yes", "vault", action, spec, "--format", "json"}, nil)
	case "reveal":
		var ref vaultRef
		if err := json.NewDecoder(body).Decode(&ref); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		spec, err := ref.spec()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		out, errOut, err := runLmice(r.Context(), []string{"vault", "reveal", spec}, nil, false, 30*time.Second)
		if err != nil {
			writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"value": string(out)})
	case "decide":
		// The Vault tab's own Allow/Deny: the signed-in session, plus the
		// request's one-time code that the list shows it.
		var req struct {
			ID     string `json:"id"`
			Code   string `json:"code"`
			Answer string `json:"answer"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		s.vaultAnswer(w, r, req.ID, req.Code, req.Answer)
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown vault action"))
	}
}

func (s *Server) vaultChange(w http.ResponseWriter, r *http.Request, args []string, stdin []byte) {
	out, errOut, err := runLmice(r.Context(), args, stdin, true, 5*time.Minute)
	if err != nil && !json.Valid(out) {
		writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
		return
	}
	s.linuxmice.mu.Lock()
	s.linuxmice.entries = nil
	s.linuxmice.mu.Unlock()
	if json.Valid(out) && len(bytes.TrimSpace(out)) > 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"detail": strings.TrimSpace(string(out))})
}

// handleVaultAnswer is the phone's Allow / Allow 1 h / Deny button (an ntfy
// http action). It needs no session: the request's one-time code, which
// only the notification carries, is the authorisation, and lmice checks it.
func (s *Server) handleVaultAnswer(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.vaultAnswer(w, r, q.Get("id"), q.Get("code"), q.Get("answer"))
}

func (s *Server) vaultAnswer(w http.ResponseWriter, r *http.Request, id, code, answer string) {
	if !vaultHexPattern.MatchString(id) || !vaultHexPattern.MatchString(code) {
		writeError(w, http.StatusBadRequest, errors.New("unknown request"))
		return
	}
	switch answer {
	case "allow", "hour", "deny":
	default:
		writeError(w, http.StatusBadRequest, errors.New("answers are allow, hour or deny"))
		return
	}
	out, errOut, err := runLmice(r.Context(), []string{"vault", "answer", id, code, answer}, nil, true, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusBadRequest, lmiceError(errOut, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"detail": strings.TrimSpace(string(out))})
}

func isVaultAnswerRoute(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/vault/answer"
}

// tokenSource re-reads DALE_TOKEN from the .env file dale was started with
// whenever the file changes, so `lmice vault rotate` takes effect without a
// restart. DALE_ENV_FILE names the file; otherwise ./.env when it holds
// DALE_TOKEN.
type tokenSource struct {
	mu       sync.Mutex
	path     string
	modified time.Time
	token    string
}

func newTokenSource(fallback string) *tokenSource {
	path := strings.TrimSpace(os.Getenv("DALE_ENV_FILE"))
	if path == "" {
		if abs, err := filepath.Abs(".env"); err == nil {
			path = abs
		}
	}
	return &tokenSource{path: path, token: fallback}
}

func (t *tokenSource) current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path == "" {
		return t.token
	}
	info, err := os.Stat(t.path)
	if err != nil || info.ModTime().Equal(t.modified) {
		return t.token
	}
	t.modified = info.ModTime()
	if token := readEnvValue(t.path, "DALE_TOKEN"); token != "" {
		t.token = token
	}
	return t.token
}

func readEnvValue(path, key string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "export ")
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}
