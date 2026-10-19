package dale

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	"backups":      {"backups", "--format", "json"},
	"org":          {"org", "--format", "json"},
	"changes":      {"changes", "--format", "json"},
	"attention":    {"attention", "--format", "json"},
	"https":        {"https", "--format", "json"},
	"hardware":     {"hardware", "compare", "--format", "json"},
	"fleetfile":    {"fleet", "file", "--format", "json"},
	"modules":      {"modules", "--format", "json"},
	"net":          {"net", "--format", "json"},
	"checkouts":    {"checkouts", "--format", "json"},
	"github":       {"github", "--format", "json"},
	"servers":      {"servers", "--format", "json"},
	"tests":        {"tests", "--format", "json"},
	"apps":         {"app", "list", "--format", "json"},
	"identity":     {"identity", "--format", "json"},
	"fleetdiff":    {"fleet", "diff", "--format", "json"},
	// Not served as a view: names this node for the page.
	"integrations-local": {"integrations", "--format", "json"},
}

var (
	// Device names start alphanumeric so they can never read as flags.
	linuxmiceDevicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	// A program name for lmice remove (lmice refuses the ones that keep a
	// device reachable).
	linuxmiceProgramPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	linuxmiceLocationPattern = regexp.MustCompile(`^~/[^\x00-\x1f]+$`)
	linuxmiceIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// An absolute or home path, no control characters, not a flag.
	linuxmicePathPattern = regexp.MustCompile(`^(/|~/)[^\x00-\x1f]{0,1000}$`)
	// A WireGuard public key (base64 of 32 bytes).
	linuxmiceWireGuardKeyPattern = regexp.MustCompile(`^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$`)
	// A tool or application name (lmice tools / app ids).
	linuxmiceToolPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// USER@HOST for joining a device.
	linuxmiceTargetPattern   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}@[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)
	linuxmiceScheduleNames   = map[string]bool{"attention": true, "backups": true, "github": true, "tests": true, "fleet": true, "https": true}
	linuxmiceServicePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,127}$`)
	linuxmiceUpstreamPattern = regexp.MustCompile(`^http://[0-9]{1,3}(\.[0-9]{1,3}){3}:[0-9]{1,5}$`)
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
	// Stale marks a cached result shown while a fresh one is being made;
	// the page asks again shortly.
	Stale bool `json:"stale,omitempty"`
}

// LinuxMiceJobRequest asks for one plan or change.
type LinuxMiceJobRequest struct {
	// plan-sync, plan-switch, plan-retire, plan-carry, execute-sync,
	// execute-switch, execute-carry, execute-upkeep, tool-update,
	// agents-start, agents-stop, agents-browser-on, agents-browser-off,
	// agents-desktop-open, agents-desktop-close (ID is the profile), backups-pull,
	// server-update, server-reboot (ID is the server),
	// org-fix (ID is the policy, Device the device),
	// projects-tidy, tool-publish, tool-install, setup-check, setup-save,
	// setup-apply, integration-enable or integration-disable.
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
	mu         sync.Mutex
	entries    map[string]linuxmiceCacheEntry
	jobs       map[string]*LinuxMiceJob
	refreshing map[string]bool
}

// warmViews are cheap and shown first; they are computed when dale starts so
// the first page load doesn't wait for them.
var warmViews = []string{"attention", "devices", "agents", "health", "tools", "backups", "projects", "integrations"}

func linuxmiceTTL(name string) time.Duration {
	if name == "map" || name == "space" || name == "upkeep" {
		return linuxmiceMapCacheTTL
	}
	return linuxmiceCacheTTL
}

// getAny returns a cached view even after it expired, and whether it is
// still fresh.
func (c *linuxmiceCache) getAny(key string) (LinuxMiceView, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return LinuxMiceView{}, false, false
	}
	return entry.view, time.Now().Before(entry.expires), true
}

// refresh recomputes a view in the background, once at a time per view.
func (c *linuxmiceCache) refresh(name, key string, args []string) {
	c.mu.Lock()
	if c.refreshing == nil {
		c.refreshing = map[string]bool{}
	}
	if c.refreshing[key] {
		c.mu.Unlock()
		return
	}
	c.refreshing[key] = true
	c.mu.Unlock()
	go func() {
		view := RunLinuxMiceView(context.Background(), name, args, linuxmiceTimeout)
		if view.Error == "" {
			c.put(key, view, linuxmiceTTL(name))
		}
		c.mu.Lock()
		delete(c.refreshing, key)
		c.mu.Unlock()
	}()
}

// Warm computes the views the first page shows, in the background.
func (c *linuxmiceCache) Warm() {
	if LinuxMiceBinary() == "" {
		return
	}
	for _, name := range warmViews {
		args := linuxmiceViews[name]
		c.refresh(name, strings.Join(args, "\x00"), args)
	}
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
	if name == "t3code-pair" {
		s.handleT3CodePair(w, r)
		return
	}
	if strings.HasPrefix(name, "identity/") {
		s.handleIdentityAction(w, r, strings.TrimPrefix(name, "identity/"))
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
	case "services", "ports", "display", "windows", "schedules", "inventory", "windows-space":
		// Without a device, lmice answers for the machine Masterdale runs on.
		command := map[string][]string{"windows-space": {"windows", "space"}}[name]
		if command == nil {
			command = []string{name}
		}
		args = append(append([]string{}, command...), "--format", "json")
		if device := query.Get("device"); device != "" {
			if !linuxmiceDevicePattern.MatchString(device) {
				writeError(w, http.StatusBadRequest, errors.New("invalid device"))
				return
			}
			args = append(append([]string{}, command...), "--device", device, "--format", "json")
		}
	case "logs":
		device, unit := query.Get("device"), query.Get("name")
		if !linuxmiceDevicePattern.MatchString(device) || !linuxmiceServicePattern.MatchString(unit) {
			writeError(w, http.StatusBadRequest, errors.New("logs needs device=DEVICE and name=SERVICE"))
			return
		}
		args = []string{"services", "logs", unit, "--device", device, "--lines", "200", "--format", "json"}
	case "identity":
		// The sign-in service's database needs write access even to read
		// (SQLite's journal), which this sandboxed service lacks: run it
		// through the user manager like a change, but it changes nothing.
		started := time.Now()
		out, errOut, err := runLmice(r.Context(), []string{"identity", "--format", "json"}, nil, true, time.Minute)
		view := LinuxMiceView{View: name, Available: LinuxMiceBinary() != "", Command: "lmice identity --format json", GeneratedAt: time.Now().UTC().Format(time.RFC3339), DurationMS: time.Since(started).Milliseconds()}
		if trimmed := bytes.TrimSpace(out); json.Valid(trimmed) && len(trimmed) > 0 {
			view.Data = json.RawMessage(trimmed)
		} else {
			view.Error = lmiceError(errOut, err).Error()
		}
		writeJSON(w, http.StatusOK, view)
		return
	case "remove-plan":
		// Only the plan: without --yes, lmice remove changes nothing.
		program := query.Get("name")
		if !linuxmiceProgramPattern.MatchString(program) {
			writeError(w, http.StatusBadRequest, errors.New("name a program, like opencode"))
			return
		}
		args = []string{"remove", program}
		for _, device := range query["device"] {
			if !linuxmiceDevicePattern.MatchString(device) {
				writeError(w, http.StatusBadRequest, errors.New("invalid device"))
				return
			}
			args = append(args, "--device", device)
		}
		if query.Get("data") == "1" {
			args = append(args, "--with-data")
		}
		args = append(args, "--format", "json")
	case "compare":
		a, b := query.Get("a"), query.Get("b")
		if !linuxmiceDevicePattern.MatchString(a) || !linuxmiceDevicePattern.MatchString(b) {
			writeError(w, http.StatusBadRequest, errors.New("compare needs a=DEVICE and b=DEVICE"))
			return
		}
		args = []string{"inventory", "compare", a, b, "--format", "json"}
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
		if view, fresh, ok := s.linuxmice.getAny(key); ok {
			// Show the last result at once; an expired one is refreshed in
			// the background and the page asks again.
			if !fresh {
				s.linuxmice.refresh(name, key, args)
				view.Stale = true
			}
			writeJSON(w, http.StatusOK, view)
			return
		}
	}
	view := RunLinuxMiceView(r.Context(), name, args, linuxmiceTimeout)
	if view.Error == "" {
		s.linuxmice.put(key, view, linuxmiceTTL(name))
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
	case "agents-start", "agents-stop", "agents-browser-on", "agents-browser-off", "agents-desktop-on", "agents-desktop-off":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		action := map[string][]string{
			"agents-start":       {"start"},
			"agents-stop":        {"stop"},
			"agents-browser-on":  {"browser", "on"},
			"agents-browser-off": {"browser", "off"},
			"agents-desktop-on":  {"desktop", "on"},
			"agents-desktop-off": {"desktop", "off"},
		}[req.Kind]
		args := append([]string{"agents"}, action...)
		return append(args, "--device", req.Device, "--yes", "--format", "json"), true, nil
	case "agents-desktop-open", "agents-desktop-close":
		if !linuxmiceIDPattern.MatchString(req.ID) || !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a profile and a device")
		}
		action := strings.TrimPrefix(req.Kind, "agents-desktop-")
		return []string{"agents", "desktop", action, req.ID, "--device", req.Device, "--yes", "--format", "json"}, true, nil
	case "server-update", "server-reboot":
		if !linuxmiceIDPattern.MatchString(req.ID) {
			return nil, false, errors.New("pick a server")
		}
		return []string{"servers", strings.TrimPrefix(req.Kind, "server-"), req.ID, "--yes", "--format", "json"}, true, nil
	case "backups-pull":
		return []string{"backups", "pull", "--yes", "--format", "json"}, true, nil
	case "org-fix":
		if req.ID != "screen-lock" && req.ID != "security-updates" {
			return nil, false, errors.New("only a screen lock or automatic security updates are fixed from here")
		}
		if !linuxmiceIDPattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		return []string{"org", "fix", req.Device, req.ID, "--yes", "--format", "json"}, true, nil
	case "tool-publish":
		if !linuxmiceIDPattern.MatchString(req.ID) {
			return nil, false, errors.New("pick a tool")
		}
		return []string{"tools", "publish", req.ID, "--yes", "--format", "json"}, true, nil
	case "tool-install":
		if !linuxmiceIDPattern.MatchString(req.ID) || !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a tool and a device")
		}
		return []string{"tools", "install", req.ID, "--device", req.Device, "--yes", "--format", "json"}, true, nil
	case "tool-update":
		if !linuxmiceIDPattern.MatchString(req.ID) || !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a tool and a device")
		}
		return []string{"tools", "update", req.ID, "--device", req.Device, "--yes", "--format", "json"}, true, nil
	case "setup-check", "setup-save", "setup-apply":
		// Device is the device to check or change; B, when set, is the device
		// whose setup file it is compared with (--like).
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		args := []string{"setup"}
		switch req.Kind {
		case "setup-save":
			args = append(args, "save")
		case "setup-apply":
			args = append(args, "apply")
		}
		args = append(args, "--device", req.Device)
		if req.Kind != "setup-save" && req.B != "" && req.B != req.Device {
			if !linuxmiceDevicePattern.MatchString(req.B) {
				return nil, false, errors.New("invalid --like device")
			}
			args = append(args, "--like", req.B)
		}
		if req.Kind == "setup-check" {
			return append(args, "--format", "json"), false, nil
		}
		return append(args, "--yes", "--format", "json"), true, nil
	case "https-apply":
		return []string{"--yes", "https", "apply"}, true, nil
	case "module-on", "module-off":
		if !linuxmiceIDPattern.MatchString(req.ID) {
			return nil, false, errors.New("pick a module")
		}
		return []string{"modules", strings.TrimPrefix(req.Kind, "module-"), req.ID}, true, nil
	case "service-restart":
		if !linuxmiceDevicePattern.MatchString(req.Device) || !linuxmiceServicePattern.MatchString(req.ID) {
			return nil, false, errors.New("pick a device and a service")
		}
		return []string{"--yes", "services", "restart", req.ID, "--device", req.Device}, true, nil
	case "fleet-capture":
		return []string{"--yes", "fleet", "capture", "--commit"}, true, nil
	case "fleet-apply":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		return []string{"--yes", "fleet", "apply", "--device", req.Device}, true, nil
	case "github-mirror":
		return []string{"--yes", "--format", "json", "github", "mirror"}, true, nil
	case "github-schedule":
		return []string{"--yes", "github", "schedule", "on"}, true, nil
	case "remove":
		// ID is the program, Device the device, A is "data" to move its
		// settings and data aside too.
		if !linuxmiceProgramPattern.MatchString(req.ID) || !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("name a program and a device")
		}
		args := []string{"--yes", "--format", "json", "remove", req.ID, "--device", req.Device}
		if req.A == "data" {
			args = append(args, "--with-data")
		}
		return args, true, nil
	case "checkouts-update":
		if req.Device == "" {
			return []string{"--yes", "--format", "json", "checkouts", "update"}, true, nil
		}
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		return []string{"--yes", "--format", "json", "checkouts", "update", "--device", req.Device}, true, nil
	case "schedule-on", "schedule-off":
		// ID is the schedule (attention, backups, github, tests, fleet, https).
		if !linuxmiceScheduleNames[req.ID] {
			return nil, false, errors.New("pick a schedule")
		}
		args := []string{"--yes", "schedules", strings.TrimPrefix(req.Kind, "schedule-"), req.ID}
		if req.Device != "" {
			if !linuxmiceDevicePattern.MatchString(req.Device) {
				return nil, false, errors.New("pick a device")
			}
			args = append(args, "--device", req.Device)
		}
		return args, true, nil
	case "fleet-send", "fleet-fetch":
		// A is the path on this machine, B the path on Device; neither
		// command overwrites anything.
		if !linuxmiceDevicePattern.MatchString(req.Device) || !linuxmicePathPattern.MatchString(req.A) || !linuxmicePathPattern.MatchString(req.B) || strings.Contains(req.A+"/"+req.B, "/../") {
			return nil, false, errors.New("pick a device and two paths (absolute or ~/...)")
		}
		if req.Kind == "fleet-send" {
			return []string{"--yes", "fleet", "send", req.A, req.Device, req.B}, true, nil
		}
		return []string{"--yes", "fleet", "fetch", req.Device, req.B, req.A}, true, nil
	case "net-add":
		// ID is the name, A the WireGuard public key, B an optional address.
		if !linuxmiceDevicePattern.MatchString(req.ID) || !linuxmiceWireGuardKeyPattern.MatchString(req.A) || (req.B != "" && net.ParseIP(req.B).To4() == nil) {
			return nil, false, errors.New("give a name, the device's public key and optionally an address")
		}
		args := []string{"--yes", "net", "add", req.ID, "--key", req.A}
		if req.B != "" {
			args = append(args, "--address", req.B)
		}
		return args, true, nil
	case "net-policy":
		return []string{"--yes", "net", "policy"}, true, nil
	case "tools-roll":
		if !linuxmiceToolPattern.MatchString(req.ID) {
			return nil, false, errors.New("pick a tool")
		}
		return []string{"--yes", "--format", "json", "tools", "roll", req.ID}, true, nil
	case "tests-run":
		return []string{"--yes", "--format", "json", "tests", "run"}, true, nil
	case "app-update":
		if !linuxmiceToolPattern.MatchString(req.ID) {
			return nil, false, errors.New("pick an application")
		}
		return []string{"--yes", "app", "update", req.ID}, true, nil
	case "backups-verify":
		return []string{"--yes", "--format", "json", "backups", "verify"}, true, nil
	case "attention-notify":
		return []string{"--yes", "attention", "notify"}, true, nil
	case "windows-ssh":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		return []string{"windows", "ssh", "--device", req.Device}, true, nil
	case "signin-app", "signin-app-remove":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		args := []string{"--format", "json", "signin", "app", "--on", req.Device}
		if req.Kind == "signin-app-remove" {
			args = append(args, "--remove")
		}
		return args, true, nil
	case "https-trust", "https-names":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		return []string{"--yes", "https", strings.TrimPrefix(req.Kind, "https-"), "--device", req.Device}, true, nil
	case "fleet-join":
		// ID is the new device's name, A its USER@HOST, B an optional jump.
		if !linuxmiceDevicePattern.MatchString(req.ID) || !linuxmiceTargetPattern.MatchString(req.A) || (req.B != "" && !linuxmiceDevicePattern.MatchString(req.B)) {
			return nil, false, errors.New("give a name and USER@HOST")
		}
		args := []string{"--yes", "fleet", "join", req.ID, "--target", req.A}
		if req.B != "" {
			args = append(args, "--via", req.B)
		}
		return args, true, nil
	case "net-join":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		return []string{"--yes", "net", "join", "--device", req.Device}, true, nil
	case "net-remove":
		if !linuxmiceDevicePattern.MatchString(req.ID) {
			return nil, false, errors.New("pick a member")
		}
		return []string{"--yes", "net", "remove", req.ID}, true, nil
	case "display-on", "display-off":
		if !linuxmiceDevicePattern.MatchString(req.Device) {
			return nil, false, errors.New("pick a device")
		}
		return []string{"--yes", "display", "virtual", strings.TrimPrefix(req.Kind, "display-"), "--device", req.Device}, true, nil
	case "https-add":
		port, err := strconv.Atoi(req.A)
		if !linuxmiceIDPattern.MatchString(req.ID) || err != nil || port < 1024 || port > 65535 || !linuxmiceUpstreamPattern.MatchString(req.B) {
			return nil, false, errors.New("give a name, a port of 1024 or above and http://PRIVATE-IP:PORT")
		}
		return []string{"--yes", "https", "add", req.ID, "--port", strconv.Itoa(port), "--to", req.B}, true, nil
	case "https-remove":
		if !linuxmiceIDPattern.MatchString(req.ID) {
			return nil, false, errors.New("pick a site")
		}
		return []string{"--yes", "https", "remove", req.ID}, true, nil
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
func runLinuxMiceWith(ctx context.Context, binary, name string, args []string, timeout time.Duration, changes ...bool) LinuxMiceView {
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
	// Changes run through the user's systemd manager when dale is sandboxed
	// (lmiceCommand); read-only views run directly.
	cmd := lmiceCommand(cctx, binary, args, len(changes) > 0 && changes[0])
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
		view := runLinuxMiceWith(context.Background(), binary, kind, args, timeout, changes)
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

// handleT3CodePair gives a signed-in person a fresh T3 Code pairing link for
// this machine (`lmice app open t3code`). The link is a five-minute
// secret: it goes straight back to the browser, which opens it, and is never
// kept in a job or a log.
func (s *Server) handleT3CodePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginJSON(r) {
		writeError(w, http.StatusForbidden, errors.New("pairing needs a same-origin JSON request"))
		return
	}
	if !s.signedIn(r) {
		writeError(w, http.StatusForbidden, errors.New("pairing needs you signed in to Masterdale (a device token or a local request is not enough)"))
		return
	}
	if !LinuxMiceActionsEnabled() {
		writeError(w, http.StatusForbidden, errors.New("changes are turned off on this node (DALE_LINUXMICE_ACTIONS)"))
		return
	}
	out, errOut, err := runLmice(r.Context(), []string{"--yes", "app", "open", "t3code", "--format", "json"}, nil, true, 60*time.Second)
	link, ok := t3codePairLink(out)
	if !ok {
		writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": link})
}

// t3codePairLink takes the pairing link out of `lmice app open --format
// json`, accepting only an https link to T3 Code's /pair page.
func t3codePairLink(out []byte) (string, bool) {
	start := bytes.IndexByte(out, '{')
	if start < 0 {
		return "", false
	}
	var response struct {
		Data struct {
			Handoff string `json:"handoff"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(out[start:])).Decode(&response); err != nil {
		return "", false
	}
	link, err := url.Parse(response.Data.Handoff)
	if err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil || link.Path != "/pair" || link.Fragment == "" {
		return "", false
	}
	return link.String(), true
}

// identityEmailPattern is an email address as lmice identity accepts it.
var identityEmailPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}@[A-Za-z0-9.-]{1,253}$`)

// identityUsernamePattern is a username, what people sign in with.
var identityUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// handleIdentityAction changes the sign-in service's accounts for a
// signed-in person: the authenticator app on (its setup key comes back to
// this browser only, once), confirm, off, and new recovery codes. Secrets
// are never kept in a job or a log.
func (s *Server) handleIdentityAction(w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginJSON(r) {
		writeError(w, http.StatusForbidden, errors.New("sign-in changes need a same-origin JSON request"))
		return
	}
	if !s.signedIn(r) {
		writeError(w, http.StatusForbidden, errors.New("sign-in changes need a signed-in browser (a device token or a local request is not enough)"))
		return
	}
	if !LinuxMiceActionsEnabled() {
		writeError(w, http.StatusForbidden, errors.New("changes are turned off on this node (DALE_LINUXMICE_ACTIONS)"))
		return
	}
	var req struct {
		// The account: its username or email.
		Email       string `json:"email"`
		Code        string `json:"code"`
		NewEmail    string `json:"new_email"`
		NewUsername string `json:"new_username"`
		Name        string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil ||
		!(identityUsernamePattern.MatchString(req.Email) || identityEmailPattern.MatchString(req.Email)) {
		writeError(w, http.StatusBadRequest, errors.New("give the account's username"))
		return
	}
	var args []string
	switch action {
	case "authenticator-on":
		args = []string{"--format", "json", "identity", "authenticator", "on", "--user", req.Email}
	case "authenticator-confirm":
		code := strings.ReplaceAll(req.Code, " ", "")
		if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
			writeError(w, http.StatusBadRequest, errors.New("the code is the 6 digits your app shows now"))
			return
		}
		args = []string{"identity", "authenticator", "confirm", "--user", req.Email, "--code", code}
	case "authenticator-off":
		args = []string{"--yes", "identity", "authenticator", "off", "--user", req.Email}
	case "recovery-codes":
		args = []string{"--yes", "--format", "json", "identity", "recovery-codes", "--user", req.Email}
	case "email":
		if !identityEmailPattern.MatchString(req.NewEmail) {
			writeError(w, http.StatusBadRequest, errors.New("give the new email address"))
			return
		}
		args = []string{"--yes", "identity", "email", "--user", req.Email, "--new-email", req.NewEmail}
		if name := strings.TrimSpace(req.Name); name != "" {
			if len(name) > 200 || strings.ContainsAny(name, "\n\r'") {
				writeError(w, http.StatusBadRequest, errors.New("a name is up to 200 characters on one line"))
				return
			}
			args = append(args, "--name", name)
		}
	case "username":
		if !identityUsernamePattern.MatchString(req.NewUsername) {
			writeError(w, http.StatusBadRequest, errors.New("a username is a-z, 0-9, '.', '_' and '-', starting with a letter or digit"))
			return
		}
		args = []string{"--yes", "identity", "username", "--user", req.Email, "--new", req.NewUsername}
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown sign-in action"))
		return
	}
	out, errOut, err := runLmice(r.Context(), args, nil, true, 60*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, lmiceError(errOut, err))
		return
	}
	s.linuxmice.mu.Lock()
	s.linuxmice.entries = nil
	s.linuxmice.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if trimmed := bytes.TrimSpace(out); json.Valid(trimmed) && len(trimmed) > 0 && trimmed[0] == '{' {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(trimmed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"detail": strings.TrimSpace(string(out))})
}
