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

// vaultAddArgs checks an add and makes its lmice arguments; the values go
// in the payload, for lmice's stdin.
func vaultAddArgs(req vaultAddRequest) ([]string, []byte, error) {
	spec, err := vaultRef{Vault: req.Vault, Name: req.Name}.spec()
	if err != nil {
		return nil, nil, err
	}
	if req.Password == "" && req.Note == "" {
		return nil, nil, errors.New("give a password or a note")
	}
	args := []string{"--yes", "vault", "add", spec, "--json"}
	if user := strings.TrimSpace(req.User); user != "" {
		if len(user) > 200 || strings.ContainsAny(user, "\r\n\x00") {
			return nil, nil, errors.New("the user name is one line")
		}
		args = append(args, "--user", user)
	}
	for _, domain := range req.Domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		if !vaultSitePattern.MatchString(domain) {
			return nil, nil, fmt.Errorf("%q is not a site (example: github.com)", domain)
		}
		args = append(args, "--domain", domain)
	}
	payload, _ := json.Marshal(map[string]string{"password": req.Password, "totp": req.TOTP, "note": req.Note})
	return args, payload, nil
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
	// A person, signed in: not the device token and not a local request,
	// which every program on this machine (an agent too) could make.
	if !s.signedIn(r) {
		writeError(w, http.StatusForbidden, errors.New("vault actions need a signed-in browser (a device token or a local request is not enough)"))
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
		args, payload, err := vaultAddArgs(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
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
	case "code":
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
		out, errOut, err := runLmice(r.Context(), []string{"vault", "code", spec}, nil, true, 30*time.Second)
		if err != nil {
			writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"value": strings.TrimSpace(string(out))})
	case "ask":
		var req struct {
			vaultRef
			On bool `json:"on"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		req.Field = ""
		spec, err := req.vaultRef.spec()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state := "off"
		if req.On {
			state = "on"
		}
		s.vaultChange(w, r, []string{"--yes", "vault", "ask", spec, state}, nil)
	case "uses":
		// How agents may use one secret (LinuxMice ADR 0080).
		var req struct {
			vaultRef
			Uses []string `json:"uses"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		req.Field = ""
		spec, err := req.vaultRef.spec()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		list := []string{}
		for _, use := range req.Uses {
			switch use {
			case "http", "fill", "code", "run":
				list = append(list, use)
			default:
				writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a use (http, fill, code, run)", use))
				return
			}
		}
		uses := "none"
		if len(list) > 0 {
			uses = strings.Join(list, ",")
		}
		s.vaultChange(w, r, []string{"--yes", "vault", "uses", spec, uses}, nil)
	case "agents":
		var req struct {
			Vault  string `json:"vault"`
			Policy string `json:"policy"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil || !vaultNamePattern.MatchString(req.Vault) {
			writeError(w, http.StatusBadRequest, errors.New("give a vault"))
			return
		}
		if req.Policy != "use" && req.Policy != "ask" && req.Policy != "never" {
			writeError(w, http.StatusBadRequest, errors.New("agents may use, ask or never"))
			return
		}
		s.vaultChange(w, r, []string{"--yes", "vault", "agents", req.Vault, req.Policy}, nil)
	case "target":
		var req struct {
			vaultRef
			Device  string `json:"device"`
			File    string `json:"file"`
			Key     string `json:"key"`
			Restart string `json:"restart"`
			Remove  bool   `json:"remove"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		req.Field = ""
		spec, err := req.vaultRef.spec()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !linuxmiceDevicePattern.MatchString(req.Device) || len(req.File) > 512 || !(strings.HasPrefix(req.File, "~/") || strings.HasPrefix(req.File, "/")) || strings.ContainsAny(req.File, ":\x00\r\n'\"") {
			writeError(w, http.StatusBadRequest, errors.New("pick a device and a file starting with ~/ or /"))
			return
		}
		args := []string{"--yes", "vault", "target", spec}
		if req.Remove {
			args = append(args, req.Device+":"+req.File, "--remove")
		} else {
			if !regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`).MatchString(req.Key) {
				writeError(w, http.StatusBadRequest, errors.New("the key is the variable name in the file, like DALE_TOKEN"))
				return
			}
			args = append(args, req.Device+":"+req.File+":"+req.Key)
			if restart := strings.TrimSpace(req.Restart); restart != "" {
				if len(restart) > 300 || strings.ContainsAny(restart, "\x00\r\n") {
					writeError(w, http.StatusBadRequest, errors.New("the restart command is one line"))
					return
				}
				args = append(args, "--restart", restart)
			}
		}
		s.vaultChange(w, r, args, nil)
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
	case "find":
		// Every settings file in the fleet that sets KEY (paths only);
		// with add, they become places the secret is copied to.
		var req struct {
			vaultRef
			Key string `json:"key"`
			Add bool   `json:"add"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		req.vaultRef.Field = ""
		spec, err := req.vaultRef.spec()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !vaultEnvKeyPattern.MatchString(req.Key) {
			writeError(w, http.StatusBadRequest, errors.New("the key is a settings name like DALE_TOKEN"))
			return
		}
		args := []string{"vault", "find", spec, "--key", req.Key}
		if req.Add {
			args = append([]string{"--yes"}, append(args, "--add")...)
		}
		out, errOut, err := runLmice(r.Context(), args, nil, req.Add, 5*time.Minute)
		if err != nil && len(bytes.TrimSpace(out)) == 0 {
			writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
			return
		}
		if req.Add {
			s.linuxmice.mu.Lock()
			s.linuxmice.entries = nil
			s.linuxmice.mu.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]string{"detail": strings.TrimSpace(string(out))})
	case "rotate-client":
		// A new secret for a sign-in client of this machine's identity
		// server, written into its settings file; it is never shown.
		var req struct {
			Client  string `json:"client"`
			Key     string `json:"key"`
			Service string `json:"service"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		if !vaultClientPattern.MatchString(req.Client) || !vaultEnvKeyPattern.MatchString(req.Key) || (req.Service != "" && !vaultClientPattern.MatchString(req.Service)) {
			writeError(w, http.StatusBadRequest, errors.New("name the client, the settings key and optionally the service"))
			return
		}
		args := []string{"--yes", "vault", "rotate-client", req.Client, "--key", req.Key, "--format", "json"}
		if req.Service != "" {
			args = append(args, "--service", req.Service)
		}
		s.vaultChange(w, r, args, nil)
	case "decide":
		// The Vault tab's own Allow/Deny: the signed-in session is the
		// authorisation (lmice accepts "-" for the code only from Masterdale).
		var req struct {
			ID     string `json:"id"`
			Answer string `json:"answer"`
		}
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		s.vaultAnswer(w, r, req.ID, "-", req.Answer)
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown vault action"))
	}
}

var (
	// A settings (environment) name, such as DALE_TOKEN.
	vaultEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// A sign-in client id or a systemd service name.
	vaultClientPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)
)

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
	if !vaultHexPattern.MatchString(id) || (code != "-" && !vaultHexPattern.MatchString(code)) {
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
