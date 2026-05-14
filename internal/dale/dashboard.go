package dale

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmdmcy/masterdale/internal/autodale"
	"github.com/mmdmcy/masterdale/internal/models"
)

type DashboardOverview struct {
	GeneratedAt string                 `json:"generated_at"`
	Node        DashboardNode          `json:"node"`
	Models      ModelConfig            `json:"models"`
	SafeRoots   []string               `json:"safe_roots"`
	Git         GitAudit               `json:"git"`
	GitSummary  map[string]int         `json:"git_summary"`
	Metrics     DashboardMetrics       `json:"metrics"`
	Fleet       DashboardFleet         `json:"fleet"`
	Events      []DashboardEvent       `json:"events"`
	EventCounts map[string]int         `json:"event_counts"`
	Actions     []DashboardAction      `json:"actions"`
	Runtime     map[string]interface{} `json:"runtime"`
}

type DashboardNode struct {
	DeviceID   string `json:"device_id"`
	Listen     string `json:"listen"`
	ServerTime string `json:"server_time"`
}

type DashboardMetrics struct {
	Samples int                     `json:"samples"`
	Latest  *autodale.MetricsSample `json:"latest,omitempty"`
	Energy  autodale.EnergyReport   `json:"energy"`
	Error   string                  `json:"error,omitempty"`
}

type DashboardFleet struct {
	Devices []FleetDevice `json:"devices,omitempty"`
	Count   int           `json:"count"`
	Online  int           `json:"online"`
	Error   string        `json:"error,omitempty"`
}

type DashboardEvent struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Actor     string `json:"actor"`
	Channel   string `json:"channel"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
}

type DashboardAction struct {
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Command string `json:"command,omitempty"`
	Level   string `json:"level"`
}

const (
	dashboardEventsTTL  = 2 * time.Second
	dashboardGitTTL     = 30 * time.Second
	dashboardMetricsTTL = 15 * time.Second
	dashboardFleetTTL   = 60 * time.Second
)

type dashboardCache struct {
	events  dashboardEventsCache
	git     dashboardGitCache
	metrics dashboardMetricsCache
	fleet   dashboardFleetCache
}

type dashboardEventsCache struct {
	mu      sync.Mutex
	events  []Event
	expires time.Time
}

type dashboardGitCache struct {
	mu      sync.Mutex
	root    string
	audit   GitAudit
	expires time.Time
}

type dashboardMetricsCache struct {
	mu      sync.Mutex
	metrics DashboardMetrics
	expires time.Time
}

type dashboardFleetCache struct {
	mu      sync.Mutex
	fleet   DashboardFleet
	expires time.Time
}

func newDashboardCache() *dashboardCache {
	return &dashboardCache{}
}

type dashboardAskRequest struct {
	Prompt         string `json:"prompt"`
	Role           string `json:"role,omitempty"`
	Model          string `json:"model,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxTokens      int    `json:"max_tokens,omitempty"`
}

type dashboardAskResponse struct {
	Model    string `json:"model"`
	Role     string `json:"role"`
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) handleDashboardRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dashboardHTML))
}

func (s *Server) handleDashboardData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	overview := s.dashboardOverview(r.Context())
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) handleDashboardAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req dashboardAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("prompt is required"))
		return
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = models.RoleContext
	}
	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 420
	}
	userEvent, err := NewEvent(s.cfg.DeviceID, "user", "chat", "message.created", map[string]any{
		"text": req.Prompt,
		"mode": "dashboard",
		"role": role,
	}, nil)
	if err == nil {
		_ = s.store.Append(userEvent)
		s.invalidateDashboardEvents()
	}
	prompt := s.dashboardAskPrompt(r.Context(), req.Prompt)
	think := false
	resp, err := CompleteWithOllama(r.Context(), s.cfg, CompleteRequest{
		Model:          strings.TrimSpace(req.Model),
		Role:           role,
		Prompt:         prompt,
		TimeoutSeconds: timeout,
		MaxTokens:      maxTokens,
		Think:          &think,
		PlainText:      true,
		KeepAlive:      "15m",
	})
	out := dashboardAskResponse{Model: resp.Model, Role: role, Response: strings.TrimSpace(resp.Response)}
	if err != nil {
		out.Error = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	if out.Response == "" {
		out.Error = "model returned an empty answer"
		writeJSON(w, http.StatusOK, out)
		return
	}
	agentEvent, err := NewEvent(s.cfg.DeviceID, "agent", "chat", "message.created", map[string]any{
		"text":  out.Response,
		"mode":  "dashboard",
		"role":  role,
		"model": out.Model,
	}, nil)
	if err == nil {
		_ = s.store.Append(agentEvent)
		s.invalidateDashboardEvents()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) dashboardOverview(ctx context.Context) DashboardOverview {
	now := time.Now()
	gitRoot := ""
	if len(s.cfg.SafeRoots) > 0 {
		gitRoot = s.cfg.SafeRoots[0]
	}
	events := s.cachedDashboardEvents(now)
	git := s.cachedDashboardGit(now, gitRoot)
	metrics := s.cachedDashboardMetrics(now)
	fleet := s.cachedDashboardFleet(ctx, now)
	overview := DashboardOverview{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Node: DashboardNode{
			DeviceID:   s.cfg.DeviceID,
			Listen:     s.cfg.Listen,
			ServerTime: now.UTC().Format(time.RFC3339Nano),
		},
		Models:      s.cfg.Models,
		SafeRoots:   append([]string{}, s.cfg.SafeRoots...),
		Git:         git,
		GitSummary:  summarizeGit(git),
		Metrics:     metrics,
		Fleet:       fleet,
		Events:      summarizeEvents(events, 18),
		EventCounts: countEvents(events),
		Runtime: map[string]interface{}{
			"remote_scope": remoteAccessScope(),
			"exec_enabled": RemoteExecEnabled(),
		},
	}
	overview.Actions = dashboardActions(overview)
	return overview
}

func (s *Server) cachedDashboardEvents(now time.Time) []Event {
	s.dashboardCache.events.mu.Lock()
	defer s.dashboardCache.events.mu.Unlock()
	if now.Before(s.dashboardCache.events.expires) {
		return append([]Event{}, s.dashboardCache.events.events...)
	}
	events, err := s.store.List(30)
	if err != nil {
		events = nil
	}
	s.dashboardCache.events.events = append([]Event{}, events...)
	s.dashboardCache.events.expires = now.Add(dashboardEventsTTL)
	return append([]Event{}, events...)
}

func (s *Server) invalidateDashboardEvents() {
	if s.dashboardCache == nil {
		return
	}
	s.dashboardCache.events.mu.Lock()
	defer s.dashboardCache.events.mu.Unlock()
	s.dashboardCache.events.expires = time.Time{}
}

func (s *Server) cachedDashboardGit(now time.Time, root string) GitAudit {
	if root == "" {
		return GitAudit{}
	}
	s.dashboardCache.git.mu.Lock()
	defer s.dashboardCache.git.mu.Unlock()
	if s.dashboardCache.git.root == root && now.Before(s.dashboardCache.git.expires) {
		return cloneGitAudit(s.dashboardCache.git.audit)
	}
	audit := AuditGitRepos(root, false)
	s.dashboardCache.git.root = root
	s.dashboardCache.git.audit = cloneGitAudit(audit)
	s.dashboardCache.git.expires = now.Add(dashboardGitTTL)
	return audit
}

func (s *Server) cachedDashboardMetrics(now time.Time) DashboardMetrics {
	s.dashboardCache.metrics.mu.Lock()
	defer s.dashboardCache.metrics.mu.Unlock()
	if now.Before(s.dashboardCache.metrics.expires) {
		return cloneDashboardMetrics(s.dashboardCache.metrics.metrics)
	}
	metrics := dashboardMetrics()
	s.dashboardCache.metrics.metrics = cloneDashboardMetrics(metrics)
	s.dashboardCache.metrics.expires = now.Add(dashboardMetricsTTL)
	return metrics
}

func (s *Server) cachedDashboardFleet(ctx context.Context, now time.Time) DashboardFleet {
	s.dashboardCache.fleet.mu.Lock()
	defer s.dashboardCache.fleet.mu.Unlock()
	if now.Before(s.dashboardCache.fleet.expires) {
		return cloneDashboardFleet(s.dashboardCache.fleet.fleet)
	}
	fleet := dashboardFleet(ctx)
	s.dashboardCache.fleet.fleet = cloneDashboardFleet(fleet)
	s.dashboardCache.fleet.expires = now.Add(dashboardFleetTTL)
	return fleet
}

func dashboardMetrics() DashboardMetrics {
	sink := autodale.DefaultSink()
	samples, err := autodale.ReadMetrics(sink)
	if err != nil {
		return DashboardMetrics{Error: err.Error()}
	}
	out := DashboardMetrics{
		Samples: len(samples),
		Energy:  autodale.DailyEnergyReport(samples, time.Now().Format("2006-01-02"), dashboardKWhCost()),
	}
	if len(samples) > 0 {
		latest := samples[len(samples)-1]
		out.Latest = &latest
	}
	return out
}

func dashboardKWhCost() float64 {
	value := os.Getenv("DALE_KWH_COST")
	if value == "" {
		value = os.Getenv("AUTODALE_KWH_COST")
	}
	if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed >= 0 {
		return parsed
	}
	return 0.40
}

func dashboardFleet(ctx context.Context) DashboardFleet {
	devices, err := TailscaleDevices(ctx)
	if err != nil {
		return DashboardFleet{Error: err.Error()}
	}
	out := DashboardFleet{Devices: devices, Count: len(devices)}
	for _, device := range devices {
		if device.Online {
			out.Online++
		}
	}
	return out
}

func cloneGitAudit(in GitAudit) GitAudit {
	out := in
	out.Repos = append([]GitRepoStatus{}, in.Repos...)
	return out
}

func cloneDashboardMetrics(in DashboardMetrics) DashboardMetrics {
	out := in
	if in.Latest != nil {
		latest := *in.Latest
		out.Latest = &latest
	}
	return out
}

func cloneDashboardFleet(in DashboardFleet) DashboardFleet {
	out := in
	out.Devices = append([]FleetDevice{}, in.Devices...)
	for i := range out.Devices {
		out.Devices[i].IPs = append([]string{}, in.Devices[i].IPs...)
	}
	return out
}

func summarizeGit(git GitAudit) map[string]int {
	out := map[string]int{"clean": 0, "dirty": 0, "behind": 0, "ahead": 0, "error": 0, "total": len(git.Repos)}
	for _, repo := range git.Repos {
		if repo.Error != "" {
			out["error"]++
		}
		if repo.Dirty > 0 {
			out["dirty"]++
		}
		if repo.Behind > 0 {
			out["behind"]++
		}
		if repo.Ahead > 0 {
			out["ahead"]++
		}
		if repo.State == "clean" {
			out["clean"]++
		}
	}
	return out
}

func summarizeEvents(events []Event, limit int) []DashboardEvent {
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	start := len(events) - limit
	if start < 0 {
		start = 0
	}
	out := make([]DashboardEvent, 0, limit)
	for i := len(events) - 1; i >= start; i-- {
		event := events[i]
		textLimit := 180
		if event.Channel == "chat" {
			textLimit = 1200
		}
		out = append(out, DashboardEvent{
			ID:        event.ID,
			Timestamp: event.Timestamp,
			Actor:     event.Actor,
			Channel:   event.Channel,
			Kind:      event.Kind,
			Text:      Shorten(TextFromBody(event.Body), textLimit),
		})
	}
	return out
}

func countEvents(events []Event) map[string]int {
	out := map[string]int{}
	for _, event := range events {
		out[event.Channel]++
	}
	return out
}

func dashboardActions(overview DashboardOverview) []DashboardAction {
	var actions []DashboardAction
	if overview.GitSummary["dirty"] > 0 {
		actions = append(actions, DashboardAction{
			Title:   "Review local Git changes",
			Detail:  fmt.Sprintf("%d repos have uncommitted changes.", overview.GitSummary["dirty"]),
			Command: "go run ./cmd/dale git audit --fetch",
			Level:   "warn",
		})
	}
	if overview.Metrics.Samples < 2 {
		actions = append(actions, DashboardAction{
			Title:   "Collect more energy samples",
			Detail:  "Long-term battery/electricity reports need more than one sample.",
			Command: "go run ./cmd/autodale monitor watch --interval 1m",
			Level:   "info",
		})
	}
	if overview.Fleet.Error != "" {
		actions = append(actions, DashboardAction{
			Title:  "Fleet discovery unavailable",
			Detail: overview.Fleet.Error,
			Level:  "info",
		})
	}
	if len(actions) == 0 {
		actions = append(actions, DashboardAction{
			Title: "No flagged checks",
			Detail: fmt.Sprintf(
				"Git dirty=%d behind=%d errors=%d; fleet online=%d/%d; metrics samples=%d.",
				overview.GitSummary["dirty"],
				overview.GitSummary["behind"],
				overview.GitSummary["error"],
				overview.Fleet.Online,
				overview.Fleet.Count,
				overview.Metrics.Samples,
			),
			Level: "ok",
		})
	}
	return actions
}

func (s *Server) dashboardAskPrompt(ctx context.Context, question string) string {
	overview := s.dashboardOverview(ctx)
	facts := map[string]any{
		"node":        overview.Node,
		"model":       map[string]any{"strategy": overview.Models.Strategy, "primary": overview.Models.Primary},
		"git_summary": overview.GitSummary,
		"energy":      overview.Metrics.Energy,
		"latest_metrics": map[string]any{
			"cpu_percent":     metricValue(overview.Metrics.Latest, "cpu"),
			"memory_free_mb":  metricValue(overview.Metrics.Latest, "memory"),
			"battery_percent": metricValue(overview.Metrics.Latest, "battery"),
			"power_source":    metricString(overview.Metrics.Latest, "power_source"),
		},
		"fleet":        map[string]any{"count": overview.Fleet.Count, "online": overview.Fleet.Online, "error": overview.Fleet.Error},
		"event_counts": overview.EventCounts,
		"actions":      overview.Actions,
	}
	b, _ := json.Marshal(facts)
	return "You are the Masterdale dashboard agent. Answer using the live dashboard facts and short repository context. Be concise. Do not claim you executed commands unless the facts say so. Suggest concrete next commands only when useful.\n\nUser question:\n" + question + "\n\nLive dashboard facts:\n" + string(b) + "\n\nRepository context:\n" + dashboardDocsContext(question)
}

func metricValue(sample *autodale.MetricsSample, field string) any {
	if sample == nil {
		return nil
	}
	switch field {
	case "cpu":
		return sample.CPUPercent
	case "memory":
		return sample.MemoryFreeMB
	case "battery":
		return sample.BatteryPercent
	default:
		return nil
	}
}

func metricString(sample *autodale.MetricsSample, field string) string {
	if sample == nil {
		return ""
	}
	if field == "power_source" {
		return sample.PowerSource
	}
	return ""
}

func dashboardDocsContext(question string) string {
	paths := dashboardContextPaths(question)
	var sections []string
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sections = append(sections, "## "+path+"\n"+Shorten(string(b), 650))
	}
	return Shorten(strings.Join(sections, "\n\n"), 3200)
}

func dashboardContextPaths(question string) []string {
	q := strings.ToLower(question)
	paths := []string{"README.md", "docs/architecture.md", "docs/operations.md", "docs/local-models.md"}
	if strings.Contains(q, "security") || strings.Contains(q, "safe") || strings.Contains(q, "token") || strings.Contains(q, "auth") {
		paths = append(paths, "docs/security.md")
	}
	if strings.Contains(q, "vision") || strings.Contains(q, "portfolio") {
		paths = append(paths, "docs/vision.md")
	}
	for _, path := range strings.Split(os.Getenv("DALE_DASHBOARD_CONTEXT_DOCS"), string(os.PathListSeparator)) {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	return uniqueStrings(paths)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
