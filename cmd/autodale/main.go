package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mmdmcy/masterdale/internal/autodale"
	"github.com/mmdmcy/masterdale/internal/dale"
	"github.com/mmdmcy/masterdale/internal/envfile"
	"github.com/mmdmcy/masterdale/internal/models"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "autodale:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if _, _, err := envfile.LoadUp(".env"); err != nil {
		return err
	}
	sink := autodale.DefaultSink()
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "check":
		if len(args) < 2 {
			usage()
			return nil
		}
		return check(sink, args[1:])
	case "task":
		return task(sink, args[1:])
	case "qa":
		if len(args) >= 2 && args[1] == "codex" {
			report, err := autodale.AnalyzeCodexHistory("", 12)
			if err == nil {
				err = sink.Emit(autodale.NewEvent(sink.DeviceID, "qa.codex", map[string]any{"report": report}))
			}
			return printJSON(report, err)
		}
	case "monitor":
		return monitor(sink, args[1:])
	case "agent":
		return agent(sink, args[1:])
	case "watch":
		return watch(sink)
	}
	usage()
	return nil
}

func monitor(sink autodale.Sink, args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	idleWatts := parseFloatFlag(args, "--idle-watts", 8)
	maxWatts := parseFloatFlag(args, "--max-watts", 35)
	switch args[0] {
	case "sample":
		sample := autodale.SampleMetrics(idleWatts, maxWatts)
		err := autodale.AppendMetric(sink, sample)
		if err == nil {
			err = sink.Emit(autodale.NewEvent(sink.DeviceID, "metrics.sampled", map[string]any{"sample": sample}))
		}
		return printJSON(sample, err)
	case "daily":
		cost := parseFloatFlag(args, "--kwh-cost", 0.40)
		day := stringFlag(args, "--day", time.Now().Format("2006-01-02"))
		samples, err := autodale.ReadMetrics(sink)
		if err != nil {
			return err
		}
		report := autodale.DailyEnergyReport(samples, day, cost)
		return printJSON(report, nil)
	case "watch":
		interval := durationFlag(args, "--interval", time.Minute)
		fmt.Println("autodale sampling metrics every", interval)
		for {
			sample := autodale.SampleMetrics(idleWatts, maxWatts)
			_ = autodale.AppendMetric(sink, sample)
			_ = sink.Emit(autodale.NewEvent(sink.DeviceID, "metrics.sampled", map[string]any{"sample": sample}))
			fmt.Printf("%s cpu=%.1f%% mem_free=%dMB watts=%.1f est=%.1f\n", sample.Timestamp, sample.CPUPercent, sample.MemoryFreeMB, sample.PowerWatts, sample.EstimatedWatts)
			time.Sleep(interval)
		}
	default:
		usage()
		return nil
	}
}

func check(sink autodale.Sink, args []string) error {
	ctx := context.Background()
	switch args[0] {
	case "selfhost":
		results := autodale.SelfhostCheck(ctx)
		err := sink.Emit(autodale.NewEvent(sink.DeviceID, "check.selfhost", map[string]any{"results": results}))
		return printJSON(results, err)
	case "ssh":
		host := firstPositional(args[1:])
		if host == "" {
			return fmt.Errorf("check ssh requires a host")
		}
		port := intFlag(args[1:], "--port", 22)
		result := autodale.SSHCheck(ctx, "ssh", host, port)
		err := sink.Emit(autodale.NewEvent(sink.DeviceID, "check.ssh", map[string]any{"result": result}))
		return printJSON(result, err)
	default:
		usage()
		return nil
	}
}

func task(sink autodale.Sink, args []string) error {
	if len(args) < 1 || args[0] != "add" {
		usage()
		return nil
	}
	after := 24 * time.Hour
	var textParts []string
	for i := 1; i < len(args); i++ {
		if args[i] == "--after" && i+1 < len(args) {
			parsed, err := time.ParseDuration(args[i+1])
			if err != nil {
				return err
			}
			after = parsed
			i++
			continue
		}
		textParts = append(textParts, args[i])
	}
	task, err := autodale.AddTask(sink, after, strings.Join(textParts, " "))
	return printJSON(task, err)
}

func watch(sink autodale.Sink) error {
	fmt.Println("autodale watching due tasks; ctrl-c to stop")
	for {
		due, err := autodale.DueTasks(sink, time.Now().UTC())
		if err != nil {
			return err
		}
		for _, task := range due {
			_ = sink.Emit(autodale.NewEvent(sink.DeviceID, "task.due", map[string]any{
				"task_id": task.ID,
				"text":    task.Text,
				"due_at":  task.DueAt,
			}))
			_ = autodale.CompleteTask(sink, task.ID)
			fmt.Println("due:", task.Text)
		}
		time.Sleep(time.Minute)
	}
}

type agentFleetReport struct {
	GeneratedAt  string                 `json:"generated_at"`
	LocalGit     dale.GitAudit          `json:"local_git"`
	LocalMetrics autodale.MetricsSample `json:"local_metrics"`
	LocalEnergy  autodale.EnergyReport  `json:"local_energy"`
	Remote       []agentRemoteReport    `json:"remote"`
	AI           agentInsight           `json:"ai"`
	AIModel      string                 `json:"ai_model,omitempty"`
	AIError      string                 `json:"ai_error,omitempty"`
}

type agentRemoteReport struct {
	Device string        `json:"device"`
	URL    string        `json:"url,omitempty"`
	OK     bool          `json:"ok"`
	Git    dale.GitAudit `json:"git,omitempty"`
	Error  string        `json:"error,omitempty"`
}

type agentInsight struct {
	Summary     string   `json:"summary"`
	GitActions  []string `json:"git_actions"`
	EnergyNotes []string `json:"energy_notes"`
	NextSteps   []string `json:"next_steps"`
}

type agentAIOptions struct {
	PreferredModel string
	Role           string
	TimeoutSeconds int
	MaxTokens      int
	Think          *bool
	Warmup         bool
	KeepAlive      string
}

func agent(sink autodale.Sink, args []string) error {
	if len(args) == 0 || args[0] != "fleet-report" {
		usage()
		return nil
	}
	cfg, err := dale.LoadOrCreateConfig("")
	if err != nil {
		return err
	}
	tail := args[1:]
	fetch := hasFlag(tail, "--fetch")
	port := intFlag(tail, "--port", 7345)
	deviceName := stringFlag(tail, "--device", "")
	day := stringFlag(tail, "--day", time.Now().Format("2006-01-02"))
	cost := parseFloatFlag(tail, "--kwh-cost", 0.40)
	aiOptions := agentOptions(tail)

	sample := autodale.SampleMetrics(parseFloatFlag(tail, "--idle-watts", 8), parseFloatFlag(tail, "--max-watts", 35))
	if err := autodale.AppendMetric(sink, sample); err != nil {
		return err
	}
	_ = sink.Emit(autodale.NewEvent(sink.DeviceID, "metrics.sampled", map[string]any{"sample": sample}))
	samples, err := autodale.ReadMetrics(sink)
	if err != nil {
		return err
	}
	report := agentFleetReport{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		LocalGit:     dale.AuditGitRepos(cfg.SafeRoots[0], fetch),
		LocalMetrics: sample,
		LocalEnergy:  autodale.DailyEnergyReport(samples, day, cost),
	}
	report.Remote = collectRemoteGit(cfg, port, deviceName, fetch)
	report.AI, report.AIModel, report.AIError = localAgentInsight(cfg, report, aiOptions)
	return printJSON(report, nil)
}

func agentOptions(args []string) agentAIOptions {
	fast := hasFlag(args, "--fast")
	opts := agentAIOptions{
		PreferredModel: strings.TrimSpace(stringFlag(args, "--model", os.Getenv("DALE_AGENT_MODEL"))),
		Role:           models.RoleStructured,
		TimeoutSeconds: 300,
		MaxTokens:      700,
		Warmup:         true,
		KeepAlive:      "15m",
	}
	if fast {
		opts.TimeoutSeconds = 45
		opts.MaxTokens = 240
		opts.PreferredModel = firstNonEmpty(opts.PreferredModel, models.FastDefault)
		opts.Role = models.RoleFast
		v := false
		opts.Think = &v
		opts.Warmup = false
	}
	if hasFlag(args, "--deep") {
		opts.Role = models.RoleReasoning
		opts.TimeoutSeconds = 600
		opts.MaxTokens = 900
	}
	if value := intFlag(args, "--ai-timeout", 0); value > 0 {
		opts.TimeoutSeconds = value
	}
	if value := intFlag(args, "--max-tokens", 0); value > 0 {
		opts.MaxTokens = value
	}
	if hasFlag(args, "--think") {
		v := true
		opts.Think = &v
	}
	if hasFlag(args, "--no-think") {
		v := false
		opts.Think = &v
	}
	if hasFlag(args, "--no-warmup") {
		opts.Warmup = false
	}
	return opts
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositional(args []string) string {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

func collectRemoteGit(cfg dale.Config, port int, deviceName string, fetch bool) []agentRemoteReport {
	var probes []dale.FleetProbe
	if deviceName != "" {
		device, err := dale.FindFleetDevice(context.Background(), deviceName)
		if err != nil {
			return []agentRemoteReport{{Device: deviceName, Error: err.Error()}}
		}
		probes = []dale.FleetProbe{dale.ProbeDevice(context.Background(), device, port)}
	} else {
		found, err := dale.ProbeFleet(context.Background(), port)
		if err != nil {
			return []agentRemoteReport{{Error: err.Error()}}
		}
		probes = found
	}

	var reports []agentRemoteReport
	for _, probe := range probes {
		item := agentRemoteReport{Device: probe.Device.HostName, URL: strings.TrimSuffix(probe.URL, "/healthz"), OK: probe.OK}
		if !probe.OK {
			item.Error = probe.Error
			reports = append(reports, item)
			continue
		}
		params := map[string]string{}
		if fetch {
			params["fetch"] = "1"
		}
		if err := remoteGetJSON(item.URL, "/v1/git/audit", params, cfg.AccessToken, &item.Git); err != nil {
			item.OK = false
			item.Error = err.Error()
		}
		reports = append(reports, item)
	}
	return reports
}

func localAgentInsight(cfg dale.Config, report agentFleetReport, opts agentAIOptions) (agentInsight, string, string) {
	b, _ := json.Marshal(agentBriefFacts(report))
	prompt := "Return only JSON with keys summary, git_actions, energy_notes, next_steps. Values: summary is a short string; the others are arrays of short strings. Rules: this is a read-only report, so never say Git operations were performed; only say what needs action. energy.daily_kwh is kWh, not money; energy.daily_cost is money. next_steps must contain 1 to 4 concrete action strings. Do not invent actions. Facts: " + string(b)
	var lastModel, lastError string
	models := agentModelCandidates(cfg, opts)
	if opts.Warmup && len(models) > 0 {
		_ = warmAgentModel(cfg, models[0], opts)
	}
	for _, model := range models {
		resp, err := dale.CompleteWithOllama(context.Background(), cfg, dale.CompleteRequest{
			Model:          model,
			Prompt:         prompt,
			TimeoutSeconds: opts.TimeoutSeconds,
			MaxTokens:      opts.MaxTokens,
			Think:          opts.Think,
			KeepAlive:      opts.KeepAlive,
		})
		lastModel = resp.Model
		if lastModel == "" {
			lastModel = model
		}
		if err != nil {
			lastError = err.Error()
			continue
		}
		var insight agentInsight
		if err := json.Unmarshal([]byte(resp.Response), &insight); err != nil {
			lastError = err.Error()
			continue
		}
		return completeInsight(report, insight), lastModel, resp.Error
	}
	return fallbackInsight(report), lastModel, lastError
}

func warmAgentModel(cfg dale.Config, model string, opts agentAIOptions) error {
	warmThink := false
	timeout := opts.TimeoutSeconds
	if timeout > 180 {
		timeout = 180
	}
	if timeout < 30 {
		timeout = 30
	}
	_, err := dale.CompleteWithOllama(context.Background(), cfg, dale.CompleteRequest{
		Model:          model,
		Prompt:         `Return only {"ok":true}.`,
		TimeoutSeconds: timeout,
		MaxTokens:      32,
		Think:          &warmThink,
		KeepAlive:      opts.KeepAlive,
	})
	return err
}

func agentModelCandidates(cfg dale.Config, opts agentAIOptions) []string {
	if strings.TrimSpace(opts.PreferredModel) != "" {
		return []string{strings.TrimSpace(opts.PreferredModel)}
	}
	raw := []string{
		cfg.Models.ForRole(opts.Role),
	}
	raw = append(raw, cfg.Models.Text, cfg.Models.Structured, cfg.Models.Context, cfg.Models.Reasoning, cfg.Models.Vision)
	raw = append(raw, cfg.Models.Fallbacks...)
	raw = append(raw, models.FastDefault)
	var out []string
	seen := map[string]bool{}
	for _, model := range raw {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	return out
}

func agentBriefFacts(report agentFleetReport) map[string]any {
	return map[string]any{
		"local_git_actions":  gitActionStrings("local", report.LocalGit),
		"remote_git_actions": remoteGitActionStrings(report.Remote),
		"energy": map[string]any{
			"host":            report.LocalMetrics.HostName,
			"battery_percent": report.LocalMetrics.BatteryPercent,
			"power_source":    report.LocalMetrics.PowerSource,
			"estimated_watts": report.LocalMetrics.EstimatedWatts,
			"energy_source":   report.LocalMetrics.EnergySource,
			"daily_samples":   report.LocalEnergy.Samples,
			"daily_kwh":       report.LocalEnergy.EnergyKWh,
			"daily_cost":      report.LocalEnergy.EstimatedCost,
			"cost_per_kwh":    report.LocalEnergy.CostPerKWh,
		},
	}
}

func gitActionStrings(prefix string, audit dale.GitAudit) []string {
	var out []string
	for _, repo := range audit.Repos {
		if repo.State != "clean" {
			action := repo.Action
			if action == "" {
				action = "inspect"
			}
			out = append(out, fmt.Sprintf("%s %s: %s; %s", prefix, repo.Name, repo.State, action))
		}
	}
	return out
}

func remoteGitActionStrings(reports []agentRemoteReport) []string {
	var out []string
	for _, remote := range reports {
		if !remote.OK {
			out = append(out, remote.Device+": "+remote.Error)
			continue
		}
		out = append(out, gitActionStrings(remote.Device, remote.Git)...)
	}
	return out
}

func completeInsight(report agentFleetReport, insight agentInsight) agentInsight {
	requiredGitActions := gitActionStrings("local", report.LocalGit)
	requiredGitActions = append(requiredGitActions, remoteGitActionStrings(report.Remote)...)
	for _, action := range requiredGitActions {
		if !containsString(insight.GitActions, action) {
			insight.GitActions = append(insight.GitActions, action)
		}
	}
	if insight.Summary == "" {
		insight.Summary = "Read-only Masterdale agent report generated."
	}
	if len(insight.EnergyNotes) == 0 {
		insight.EnergyNotes = fallbackInsight(report).EnergyNotes
	}
	if len(insight.NextSteps) == 0 {
		insight.NextSteps = fallbackInsight(report).NextSteps
	}
	return insight
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func fallbackInsight(report agentFleetReport) agentInsight {
	var gitActions []string
	for _, repo := range report.LocalGit.Repos {
		if repo.State != "clean" {
			gitActions = append(gitActions, "local "+repo.Name+": "+repo.State+"; "+repo.Action)
		}
	}
	for _, remote := range report.Remote {
		if !remote.OK {
			gitActions = append(gitActions, remote.Device+": "+remote.Error)
			continue
		}
		for _, repo := range remote.Git.Repos {
			if repo.State != "clean" {
				gitActions = append(gitActions, remote.Device+" "+repo.Name+": "+repo.State+"; "+repo.Action)
			}
		}
	}
	energy := []string{"sampled " + report.LocalMetrics.HostName + "; energy source is " + report.LocalMetrics.EnergySource}
	if report.LocalEnergy.Samples < 2 {
		energy = append(energy, "daily kWh needs at least two samples over time")
	}
	return agentInsight{
		Summary:     "Generated from deterministic Masterdale checks; local AI summary was unavailable.",
		GitActions:  gitActions,
		EnergyNotes: energy,
		NextSteps:   []string{"pull repos marked behind", "repair repos marked no-upstream", "keep monitor watch running for long-term energy totals"},
	}
}

func remoteGetJSON(baseURL string, path string, params map[string]string, token string, out any) error {
	values := url.Values{}
	for key, value := range params {
		if value != "" {
			values.Set(key, value)
		}
	}
	u := baseURL + path
	if encoded := values.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func printJSON(v any, err error) error {
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func parseFloatFlag(args []string, name string, fallback float64) float64 {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			value, err := strconv.ParseFloat(args[i+1], 64)
			if err == nil {
				return value
			}
		}
	}
	return fallback
}

func stringFlag(args []string, name string, fallback string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return fallback
}

func durationFlag(args []string, name string, fallback time.Duration) time.Duration {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			value, err := time.ParseDuration(args[i+1])
			if err == nil {
				return value
			}
		}
	}
	return fallback
}

func intFlag(args []string, name string, fallback int) int {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			value, err := strconv.Atoi(args[i+1])
			if err == nil {
				return value
			}
		}
	}
	return fallback
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}

func usage() {
	fmt.Println(`autodale commands:
  agent fleet-report [--device name] [--fetch] [--kwh-cost 0.40] [--model name] [--ai-timeout seconds] [--max-tokens n] [--think|--no-think] [--fast|--deep]
  check selfhost
  check ssh <host> [--port 22]
  monitor sample [--idle-watts 8] [--max-watts 35]
  monitor daily [--day YYYY-MM-DD] [--kwh-cost 0.40]
  monitor watch [--interval 1m] [--idle-watts 8] [--max-watts 35]
  task add [--after 24h] <text>
  qa codex
  watch`)
}
