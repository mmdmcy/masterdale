package dale

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var tailscaleNetworks = []*net.IPNet{
	mustCIDR("100.64.0.0/10"),
	mustCIDR("fd7a:115c:a1e0::/48"),
}

var privateNetworks = []*net.IPNet{
	mustCIDR("10.0.0.0/8"),
	mustCIDR("172.16.0.0/12"),
	mustCIDR("192.168.0.0/16"),
	mustCIDR("169.254.0.0/16"),
	mustCIDR("fc00::/7"),
	mustCIDR("fe80::/10"),
}

type Server struct {
	cfg            Config
	store          *Store
	mux            *http.ServeMux
	dashboardCache *dashboardCache
}

func NewServer(cfg Config, store *Store) *Server {
	s := &Server{cfg: cfg, store: store, mux: http.NewServeMux(), dashboardCache: newDashboardCache()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.mux)
}

func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.cfg.Listen, s.Handler())
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remoteNetworkAllowed(r) {
			writeError(w, http.StatusForbidden, errors.New("remote access is restricted to loopback and private-network addresses by default; set DALE_REMOTE_SCOPE=public only if a firewall or reverse proxy protects this service"))
			return
		}
		if r.URL.Path == "/healthz" || isDashboardShellRequest(r) || isLoopbackRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.AccessToken == "" {
			writeError(w, http.StatusUnauthorized, errors.New("authorization required"))
			return
		}
		if !validBearerToken(r.Header.Get("Authorization"), s.cfg.AccessToken) {
			writeError(w, http.StatusUnauthorized, errors.New("authorization required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isDashboardShellRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	return r.URL.Path == "/" || r.URL.Path == "/dashboard"
}

func remoteNetworkAllowed(r *http.Request) bool {
	ip := requestIP(r)
	if ip == nil {
		return false
	}
	switch remoteAccessScope() {
	case "loopback":
		return ip.IsLoopback()
	case "tailnet":
		return ip.IsLoopback() || isTailscaleIP(ip)
	case "public":
		return true
	default:
		return ip.IsLoopback() || isPrivateNetworkIP(ip)
	}
}

func isLoopbackRequest(r *http.Request) bool {
	ip := requestIP(r)
	return ip != nil && ip.IsLoopback()
}

func requestIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func remoteAccessScope() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DALE_REMOTE_SCOPE")))
	switch v {
	case "", "private":
		return "private"
	case "loopback", "local":
		return "loopback"
	case "tailnet", "tailscale":
		return "tailnet"
	case "public", "all", "any":
		return "public"
	default:
		return "private"
	}
}

func isTailscaleIP(ip net.IP) bool {
	for _, network := range tailscaleNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func isPrivateNetworkIP(ip net.IP) bool {
	if isTailscaleIP(ip) {
		return true
	}
	for _, network := range privateNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func validBearerToken(header string, token string) bool {
	if token == "" || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	got := strings.TrimPrefix(header, "Bearer ")
	if len(got) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func mustCIDR(value string) *net.IPNet {
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		panic(err)
	}
	return network
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleDashboardRoot)
	s.mux.HandleFunc("/dashboard", s.handleDashboard)
	s.mux.HandleFunc("/v1/dashboard", s.handleDashboardData)
	s.mux.HandleFunc("/v1/dashboard/ask", s.handleDashboardAsk)
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/v1/events", s.handleEvents)
	s.mux.HandleFunc("/v1/resources/search", s.handleSearch)
	s.mux.HandleFunc("/v1/tasks", s.handleTasks)
	s.mux.HandleFunc("/v1/llm/complete", s.handleComplete)
	s.mux.HandleFunc("/v1/node/report", s.handleNodeReport)
	s.mux.HandleFunc("/v1/scans/npm", s.handleNPMScan)
	s.mux.HandleFunc("/v1/fs/list", s.handleFSList)
	s.mux.HandleFunc("/v1/fs/read", s.handleFSRead)
	s.mux.HandleFunc("/v1/fs/search", s.handleFSSearch)
	s.mux.HandleFunc("/v1/git/audit", s.handleGitAudit)
	s.mux.HandleFunc("/v1/exec", s.handleExec)
	s.mux.HandleFunc("/mcp", s.handleMCP)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		events, err := s.store.List(limit)
		writeResult(w, events, err)
	case http.MethodPost:
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if e.DeviceID == "" {
			e.DeviceID = s.cfg.DeviceID
		}
		if e.Schema == "" {
			e.Schema = EventSchema
		}
		if e.Timestamp == "" {
			e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if err := s.store.Append(e); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.invalidateDashboardEvents()
		writeJSON(w, http.StatusCreated, e)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 25
	}
	results, err := s.store.Search(r.URL.Query().Get("q"), limit)
	writeResult(w, results, err)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	e, err := NewEvent(s.cfg.DeviceID, "user", "task", "task.created", body, nil)
	if err == nil {
		err = s.store.Append(e)
	}
	writeResult(w, e, err)
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req CompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resp, err := CompleteWithOllama(r.Context(), s.cfg, req)
	writeResult(w, resp, err)
}

func (s *Server) handleNodeReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":   s.cfg.DeviceID,
		"safe_roots":  s.cfg.SafeRoots,
		"ollama_url":  s.cfg.OllamaURL,
		"server_time": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) handleNPMScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	packages := r.URL.Query()["package"]
	if len(packages) == 0 {
		if pkg := r.URL.Query().Get("q"); pkg != "" {
			packages = []string{pkg}
		}
	}
	roots := r.URL.Query()["root"]
	if len(roots) == 0 {
		roots = s.cfg.SafeRoots
	}
	writeJSON(w, http.StatusOK, ScanNPM(roots, packages, 5000))
}

func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	entries, err := ListFiles(s.cfg.SafeRoots, r.URL.Query().Get("path"))
	writeResult(w, entries, err)
}

func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	maxBytes, _ := strconv.ParseInt(r.URL.Query().Get("max_bytes"), 10, 64)
	result, err := ReadFileSafe(s.cfg.SafeRoots, r.URL.Query().Get("path"), maxBytes)
	writeResult(w, result, err)
}

func (s *Server) handleFSSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	maxFiles, _ := strconv.Atoi(r.URL.Query().Get("max_files"))
	maxMatches, _ := strconv.Atoi(r.URL.Query().Get("max_matches"))
	result := SearchFiles(s.cfg.SafeRoots, r.URL.Query().Get("root"), r.URL.Query().Get("q"), maxFiles, maxMatches)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !RemoteExecEnabled() && !isLoopbackRequest(r) {
		writeError(w, http.StatusForbidden, errors.New("remote exec disabled; set DALE_REMOTE_EXEC=1 on this device to enable"))
		return
	}
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := RunCommand(r.Context(), s.cfg, req)
	writeResult(w, result, err)
}

func (s *Server) handleGitAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	root, err := safePath(s.cfg.SafeRoots, r.URL.Query().Get("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	fetch := r.URL.Query().Get("fetch") == "1" || strings.EqualFold(r.URL.Query().Get("fetch"), "true")
	writeJSON(w, http.StatusOK, AuditGitRepos(root, fetch))
}

func writeResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func cleanArg(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
