package dale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// LinuxMice integration: the dashboard shows read-only `lmice` views by
// running the CLI and passing its JSON through unchanged. LinuxMice stays
// optional; without an lmice binary the views report available=false.

const (
	linuxmiceTimeout  = 3 * time.Minute
	linuxmiceCacheTTL = 60 * time.Second
	// The map visits every fleet device and takes about a minute.
	linuxmiceMapCacheTTL = 5 * time.Minute
	linuxmiceOutputLimit = 8 << 20
)

// linuxmiceViews maps each view to fixed, read-only lmice arguments.
var linuxmiceViews = map[string][]string{
	"map":          {"workspace", "map", "--format", "json"},
	"integrations": {"integrations", "--fleet", "--format", "json"},
	"selfhost":     {"selfhost", "--format", "json"},
}

var (
	// Device names start alphanumeric so they can never read as flags.
	linuxmiceDevicePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	linuxmiceLocationPattern = regexp.MustCompile(`^~/[^\x00-\x1f]+$`)
)

type LinuxMiceView struct {
	View        string          `json:"view"`
	Available   bool            `json:"available"`
	Command     string          `json:"command,omitempty"`
	GeneratedAt string          `json:"generated_at"`
	DurationMS  int64           `json:"duration_ms"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
	Hint        string          `json:"hint,omitempty"`
}

type linuxmiceCache struct {
	mu      sync.Mutex
	entries map[string]linuxmiceCacheEntry
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
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/v1/linuxmice/")
	query := r.URL.Query()
	var args []string
	switch name {
	case "diff":
		location, a, b := query.Get("location"), query.Get("a"), query.Get("b")
		if len(location) > 1024 || !linuxmiceLocationPattern.MatchString(location) || !linuxmiceDevicePattern.MatchString(a) || !linuxmiceDevicePattern.MatchString(b) {
			writeError(w, http.StatusBadRequest, errors.New("diff needs location=~/PATH, a=DEVICE and b=DEVICE"))
			return
		}
		args = []string{"workspace", "diff", location, "--between", a, b, "--format", "json"}
	default:
		fixed, ok := linuxmiceViews[name]
		if !ok {
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
	view := RunLinuxMiceView(r.Context(), name, args)
	if view.Error == "" {
		ttl := linuxmiceCacheTTL
		if name == "map" {
			ttl = linuxmiceMapCacheTTL
		}
		s.linuxmice.put(key, view, ttl)
	}
	writeJSON(w, http.StatusOK, view)
}

// RunLinuxMiceView runs one read-only lmice command and keeps its JSON.
func RunLinuxMiceView(ctx context.Context, name string, args []string) LinuxMiceView {
	view := LinuxMiceView{View: name, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	binary := LinuxMiceBinary()
	if binary == "" {
		view.Hint = "install lmice (LinuxMice) into ~/.local/bin to see fleet workspace views here"
		return view
	}
	view.Available = true
	view.Command = "lmice " + strings.Join(args, " ")
	cctx, cancel := context.WithTimeout(ctx, linuxmiceTimeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(cctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	view.DurationMS = time.Since(start).Milliseconds()
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
	if strings.Contains(view.Error, "unrecognized subcommand") {
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
