package dale

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// LinuxMice integration: the /fleet page shows `lmice` views by running the
// CLI and passing its JSON through unchanged, so everything the page shows
// or does is one lmice command an operator or agent can run directly.
// LinuxMice stays optional; without an lmice binary views report
// available=false.
//
// Read-only views are GET requests. Plans and changes run as background jobs
// because a sync can take minutes. Changes (sync or carry with --execute,
// integration opt-outs) also need DALE_LINUXMICE_ACTIONS=1 on the server, and
// only one change runs at a time.

const (
	linuxmiceTimeout  = 3 * time.Minute
	linuxmiceCacheTTL = 60 * time.Second
	// The map visits every fleet device and takes about a minute.
	linuxmiceMapCacheTTL = 5 * time.Minute
	linuxmiceOutputLimit = 8 << 20
	linuxmicePlanTimeout = 20 * time.Minute
	linuxmiceRunTimeout  = 6 * time.Hour
	linuxmiceJobsKept    = 20
)

// linuxmiceViews maps each view to fixed, read-only lmice arguments.
var linuxmiceViews = map[string][]string{
	"map":          {"workspace", "map", "--format", "json"},
	"integrations": {"integrations", "--fleet", "--format", "json"},
	"selfhost":     {"selfhost", "--format", "json"},
	"devices":      {"fleet", "status", "--format", "json"},
	"rescue":       {"rescue", "status", "--format", "json"},
	"health":       {"health", "--fleet", "--format", "json"},
	"tools":        {"tools", "--fleet", "--format", "json"},
	"upkeep":       {"upkeep", "--format", "json"},
	"agents":       {"agents", "--format", "json"},
	"projects":     {"projects", "--format", "json"},
	"space":        {"space", "--fleet", "--format", "json"},
	// Not served as a view: names this node for the page.
	"integrations-local": {"integrations", "--format", "json"},
}

var (
	// Device names start alphanumeric so they can never read as flags.
	linuxmiceDevicePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	linuxmiceLocationPattern = regexp.MustCompile(`^~/[^\x00-\x1f]+$`)
	linuxmiceIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type LinuxMiceView struct {
	View        string          `json:"view"`
	Available   bool            `json:"available"`
	Command     string          `json:"command,omitempty"`
	GeneratedAt string          `json:"generated_at"`
	DurationMS  int64           `json:"duration_ms"`
	ExitCode    int             `json:"exit_code"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
	Hint        string          `json:"hint,omitempty"`
}

// LinuxMiceJobRequest asks for one plan or change.
type LinuxMiceJobRequest struct {
	// plan-sync, plan-switch, plan-retire, plan-carry, execute-sync,
	// execute-switch, execute-carry, execute-upkeep, tool-update,
	// agents-start, agents-stop, agents-browser-on, agents-browser-off,
	// projects-tidy,
	// integration-enable or integration-disable.
	Kind   string `json:"kind"`
	A      string `json:"a,omitempty"`
	B      string `json:"b,omitempty"`
	Device string `json:"device,omitempty"`
	ID     string `json:"id,omitempty"`
}

type LinuxMiceJob struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"`
	Command    string        `json:"command"`
	Changes    bool          `json:"changes"`
	State      string        `json:"state"`
	StartedAt  string        `json:"started_at"`
	FinishedAt string        `json:"finished_at,omitempty"`
	View       LinuxMiceView `json:"view"`
}

type linuxmiceCache struct {
	mu      sync.Mutex
	entries map[string]linuxmiceCacheEntry
	jobs    map[string]*LinuxMiceJob
}

type linuxmiceCacheEntry struct {
	view    LinuxMiceView
	expires time.Time
}

func (s *Server) handleFleetPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(fleetHTML))
}

func (s *Server) handleLinuxMice(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/v1/linuxmice/")
	if name == "jobs" || strings.HasPrefix(name, "jobs/") {
		s.handleLinuxMiceJobs(w, r, strings.TrimPrefix(strings.TrimPrefix(name, "jobs"), "/"))
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	var args []string
	switch name {
	case "capabilities":
		// The fleet name LinuxMice uses for this node, which can differ from
		// Masterdale's device id (for example a DHCP hostname on macOS).
		device := s.cfg.DeviceID
		local := s.linuxmiceLocal(r.Context())
		if local != "" {
			device = local
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"available":       LinuxMiceBinary() != "",
			"actions_enabled": LinuxMiceActionsEnabled(),
			"device":          device,
		})
		return
	case "diff":
		location, a, b := query.Get("location"), query.Get("a"), query.Get("b")
		if len(location) > 1024 || !linuxmiceLocationPattern.MatchString(location) || !linuxmiceDevicePattern.MatchString(a) || !linuxmiceDevicePattern.MatchString(b) {
			writeError(w, http.StatusBadRequest, errors.New("diff needs location=~/PATH, a=DEVICE and b=DEVICE"))
			return
		}
		args = []string{"workspace", "diff", location, "--between", a, b}
		if query.Get("patch") == "1" {
			args = append(args, "--patch")
		}
		args = append(args, "--format", "json")
	case "selfhost":
		args = []string{"selfhost", "--format", "json"}
		if device := query.Get("device"); device != "" {
			if !linuxmiceDevicePattern.MatchString(device) {
				writeError(w, http.StatusBadRequest, errors.New("invalid device"))
				return
			}
			args = []string{"selfhost", "--device", device, "--format", "json"}
		}
	default:
		fixed, ok := linuxmiceViews[name]
		if !ok || name == "integrations-local" {
			writeError(w, http.StatusNotFound, errors.New("unknown LinuxMice view"))
			return
		}
		args = fixed
	}
	key := strings.Join(args, "\x00")
	if query.Get("refresh") == "" {
		if view, ok := s.linuxmice.get(key); ok {
			writeJSON(w, http.StatusOK, view)
			return
		}
	}
	view := RunLinuxMiceView(r.Context(), name, args, linuxmiceTimeout)
	if view.Error == "" {
		ttl := linuxmiceCacheTTL
		if name == "map" {
			ttl = linuxmiceMapCacheTTL
		}
		s.linuxmice.put(key, view, ttl)
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleLinuxMiceJobs(w http.ResponseWriter, r *http.Request, id string) {
	switch {
	case r.Method == http.MethodGet && id == "":
		writeJSON(w, http.StatusOK, s.linuxmice.listJobs())
	case r.Method == http.MethodGet:
		job, ok := s.linuxmice.job(id)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("unknown job"))
			return
		}
		writeJSON(w, http.StatusOK, job)
	case r.Method == http.MethodPost && id == "":
		if !sameOriginJSON(r) {
			writeError(w, http.StatusForbidden, errors.New("LinuxMice jobs need a same-origin JSON request"))
			return
		}
		var req LinuxMiceJobRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		args, changes, err := linuxmiceJobArgs(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if changes && !LinuxMiceActionsEnabled() {
			writeError(w, http.StatusForbidden, errors.New("changes are turned off on this node; set DALE_LINUXMICE_ACTIONS=1 to allow them, or run the command in a terminal"))
			return
		}
		job, err := s.linuxmice.start(req.Kind, args, changes)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// linuxmiceJobArgs turns a request into fixed lmice arguments; it reports
// whether the command changes anything.
func linuxmiceJobArgs(req LinuxMiceJobRequest) ([]string, bool, error) {
	pair := func() error {
		if !linuxmiceDevicePattern.MatchString(req.A) || !linuxmiceDevicePattern.MatchString(req.B) || req.A == req.B {
			return errors.New("pick two different devices (a and b)")
		}
		return nil
	}
	switch req.Kind {
	case "plan-sync", "execute-sync":
		if err := pair(); err != nil {
			return nil, false, err
		}
		args := []string{"workspace", "sync", req.A, req.B, "--format", "json"}
		if req.Kind == "execute-sync" {
			return append(args, "--execute"), true, nil
		}
		return args, false, nil
	case "plan-switch", "execute-switch":
		if err := pair(); err != nil {
			return nil, false, err
		}
		args := []string{"workspace", "switch", req.B, "--from", req.A, "--format", "json"}
		if req.Kind == "execute-switch" {
			return append(args, "--execute"), true, nil
		}
		return args, false, nil
	case "plan-retire":
		if err := pair(); err != nil {
			return nil, false, err
		}
		return []string{"workspace", "retire", req.A, "--to", req.B, "--format", "json"}, false, nil
	case "plan-carry", "execute-carry":
		if err := pair(); err != nil {
			return nil, false, err
		}
		args := []string{"workspace", "carry", req.A, "--to", req.B, "--format", "json"}
		if req.Kind == "execute-carry" {
			return append(args, "--execute"), true, nil
		}
		return args, false, nil
	case "execute-upkeep":
		return []string{"upkeep", "--yes", "--format", "json"}, true, nil
	case "projects-tidy":
		return []string{"projects", "tidy", "--yes", "--format", "json"}, true, nil
	case "agents-start", "agents-stop", "agents-browser-on", "agents-browser-off":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		action := map[string][]string{
			"agents-start":       {"start"},
			"agents-stop":        {"stop"},
			"agents-browser-on":  {"browser", "on"},
			"agents-browser-off": {"browser", "off"},
		}[req.Kind]
		args := append([]string{"agents"}, action...)
		return append(args, "--device", req.Device, "--yes", "--format", "json"), true, nil
	case "tool-update":
		if !linuxmiceIDPattern.MatchString(req.ID) || !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a tool and a device")
		}
		return []string{"tools", "update", req.ID, "--device", req.Device, "--yes", "--format", "json"}, true, nil
	case "integration-enable", "integration-disable":
		if !linuxmiceIDPattern.MatchString(req.ID) || !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick an integration id and a device")
		}
		action := strings.TrimPrefix(req.Kind, "integration-")
		return []string{"integrations", action, req.ID, "--device", req.Device, "--format", "json"}, true, nil
	}
	return nil, false, errors.New("unknown job kind")
}

// sameOriginJSON rejects cross-site form posts: the body must be JSON, and a
// browser's Origin header must name this host.
// linuxmiceLocal returns this node's LinuxMice device name, cached like a view.
func (s *Server) linuxmiceLocal(ctx context.Context) string {
	args := linuxmiceViews["integrations-local"]
	key := strings.Join(args, "\x00")
	view, ok := s.linuxmice.get(key)
	if !ok {
		view = RunLinuxMiceView(ctx, "integrations-local", args, 30*time.Second)
		if view.Error != "" {
			return ""
		}
		s.linuxmice.put(key, view, linuxmiceMapCacheTTL)
	}
	var report struct {
		Device string `json:"device"`
	}
	if json.Unmarshal(view.Data, &report) != nil {
		return ""
	}
	return report.Device
}

func sameOriginJSON(r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}

func LinuxMiceActionsEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("DALE_LINUXMICE_ACTIONS")))
	return value == "1" || value == "true" || value == "yes"
}

// RunLinuxMiceView runs one lmice command and keeps its JSON.
func RunLinuxMiceView(ctx context.Context, name string, args []string, timeout time.Duration) LinuxMiceView {
	return runLinuxMiceWith(ctx, LinuxMiceBinary(), name, args, timeout)
}

// runLinuxMiceWith runs one lmice command with a binary chosen by the caller,
// so a background job keeps the binary it was started with.
func runLinuxMiceWith(ctx context.Context, binary, name string, args []string, timeout time.Duration) LinuxMiceView {
	view := LinuxMiceView{View: name, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	if binary == "" {
		view.Hint = "install lmice (LinuxMice) into ~/.local/bin to see fleet workspace views here"
		return view
	}
	view.Available = true
	view.Command = "lmice " + strings.Join(args, " ")
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(cctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	view.DurationMS = time.Since(start).Milliseconds()
	if cmd.ProcessState != nil {
		view.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stdout.Len() > linuxmiceOutputLimit {
		view.Error = "lmice output exceeded the dashboard limit"
		return view
	}
	// Some lmice commands report a verdict through their exit status but
	// still print a complete JSON report; keep it.
	if json.Valid(stdout.Bytes()) && stdout.Len() > 0 {
		view.Data = json.RawMessage(stdout.Bytes())
		return view
	}
	view.Error = strings.TrimSpace(Shorten(stderr.String(), 4096))
	if view.Error == "" && err != nil {
		view.Error = err.Error()
	}
	if strings.Contains(view.Error, "unrecognized subcommand") || strings.Contains(view.Error, "unexpected argument") {
		view.Hint = "this lmice is older than the view; install a current build"
	}
	return view
}

// LinuxMiceBinary finds lmice: DALE_LMICE, then PATH, then the usual
// per-user and system locations (services run without a login PATH).
func LinuxMiceBinary() string {
	if path := strings.TrimSpace(os.Getenv("DALE_LMICE")); path != "" {
		return path
	}
	if path, err := exec.LookPath("lmice"); err == nil {
		return path
	}
	candidates := []string{"/usr/local/bin/lmice", "/usr/bin/lmice"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append([]string{filepath.Join(home, ".local", "bin", "lmice")}, candidates...)
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path
		}
	}
	return ""
}

func (c *linuxmiceCache) get(key string) (LinuxMiceView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expires) {
		return LinuxMiceView{}, false
	}
	return entry.view, true
}

func (c *linuxmiceCache) put(key string, view LinuxMiceView, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]linuxmiceCacheEntry{}
	}
	c.entries[key] = linuxmiceCacheEntry{view: view, expires: time.Now().Add(ttl)}
}

// start runs a job in the background; a change is refused while another
// change is still running.
func (c *linuxmiceCache) start(kind string, args []string, changes bool) (LinuxMiceJob, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jobs == nil {
		c.jobs = map[string]*LinuxMiceJob{}
	}
	if changes {
		for _, job := range c.jobs {
			if job.Changes && job.State == "running" {
				return LinuxMiceJob{}, errors.New("another change is still running: " + job.Command)
			}
		}
	}
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	job := &LinuxMiceJob{
		ID:        hex.EncodeToString(raw),
		Kind:      kind,
		Command:   "lmice " + strings.Join(args, " "),
		Changes:   changes,
		State:     "running",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	c.jobs[job.ID] = job
	c.pruneLocked()
	timeout := linuxmicePlanTimeout
	if changes {
		timeout = linuxmiceRunTimeout
	}
	// Resolved now, not when the goroutine runs: the job must use the lmice
	// it was started with even if the environment changes meanwhile.
	binary := LinuxMiceBinary()
	go func() {
		// Changes must finish even if the browser goes away.
		view := runLinuxMiceWith(context.Background(), binary, kind, args, timeout)
		c.mu.Lock()
		defer c.mu.Unlock()
		job.View = view
		job.State = "done"
		if view.Error != "" {
			job.State = "failed"
		}
		job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if changes {
			// Whatever the views showed may be stale now.
			c.entries = nil
		}
	}()
	return *job, nil
}

func (c *linuxmiceCache) pruneLocked() {
	if len(c.jobs) <= linuxmiceJobsKept {
		return
	}
	finished := make([]*LinuxMiceJob, 0, len(c.jobs))
	for _, job := range c.jobs {
		if job.State != "running" {
			finished = append(finished, job)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].StartedAt < finished[j].StartedAt })
	for _, job := range finished {
		if len(c.jobs) <= linuxmiceJobsKept {
			return
		}
		delete(c.jobs, job.ID)
	}
}

func (c *linuxmiceCache) job(id string) (LinuxMiceJob, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	job, ok := c.jobs[id]
	if !ok {
		return LinuxMiceJob{}, false
	}
	return *job, true
}

func (c *linuxmiceCache) listJobs() []LinuxMiceJob {
	c.mu.Lock()
	defer c.mu.Unlock()
	jobs := make([]LinuxMiceJob, 0, len(c.jobs))
	for _, job := range c.jobs {
		copy := *job
		copy.View.Data = nil
		jobs = append(jobs, copy)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt > jobs[j].StartedAt })
	return jobs
}
