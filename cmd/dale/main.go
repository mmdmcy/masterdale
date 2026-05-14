package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mmdmcy/masterdale/internal/dale"
	"github.com/mmdmcy/masterdale/internal/envfile"
	"github.com/mmdmcy/masterdale/internal/models"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dale:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if _, _, err := envfile.LoadUp(".env"); err != nil {
		return err
	}
	if len(args) >= 1 && args[0] == "env" {
		return envCommand(args[1:])
	}
	cfg, err := dale.LoadOrCreateConfig("")
	if err != nil {
		return err
	}
	store, err := dale.OpenStore(cfg)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "init":
		fmt.Println("data:", cfg.DataDir)
		fmt.Println("device:", cfg.DeviceID)
		return nil
	case "up":
		return up(cfg, args[1:])
	case "down":
		return down(cfg)
	case "status":
		return status(args[1:])
	case "token":
		fmt.Println(cfg.AccessToken)
		return nil
	case "serve":
		if len(args) > 2 && args[1] == "--listen" {
			cfg.Listen = args[2]
		}
		fmt.Println("daled listening on http://" + cfg.Listen)
		return dale.NewServer(cfg, store).ListenAndServe()
	case "chat":
		return chat(cfg, store, args[1:])
	case "ask":
		return ask(cfg, store, args[1:])
	case "context":
		if len(args) >= 3 && args[1] == "search" {
			return search(store, strings.Join(args[2:], " "))
		}
	case "sessions":
		if len(args) >= 2 && args[1] == "ingest" {
			report, err := dale.IngestCodex(cfg, store, "")
			if err != nil {
				return err
			}
			return printJSON(report)
		}
	case "models":
		if len(args) >= 2 && args[1] == "bench" {
			return benchModels(cfg)
		}
	case "npm":
		if len(args) >= 2 && args[1] == "scan" {
			return npmScan(cfg, args[2:])
		}
	case "git":
		return gitCommand(cfg, args[1:])
	case "fleet":
		return fleet(cfg, args[1:])
	case "remote":
		return remote(cfg, args[1:])
	case "devices":
		return devices()
	}
	printUsage()
	return nil
}

func remote(cfg dale.Config, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	sub := args[0]
	tail := args[1:]
	port := parsePort(tail, 7345)
	baseURL, err := remoteBaseURL(tail, port)
	if err != nil {
		return err
	}
	token := cfg.AccessToken
	switch sub {
	case "list":
		path := firstPositional(tail)
		var entries []dale.FileEntry
		if err := remoteGET(baseURL, "/v1/fs/list", map[string]string{"path": path}, token, &entries); err != nil {
			return err
		}
		for _, entry := range entries {
			kind := "file"
			if entry.IsDir {
				kind = "dir"
			}
			fmt.Printf("%-4s %10d %s\n", kind, entry.Size, entry.Path)
		}
		return nil
	case "read":
		path := firstPositional(tail)
		var result dale.FileReadResult
		if err := remoteGET(baseURL, "/v1/fs/read", map[string]string{"path": path}, token, &result); err != nil {
			return err
		}
		if result.Encoding == "utf-8" {
			fmt.Print(result.Content)
			if !strings.HasSuffix(result.Content, "\n") {
				fmt.Println()
			}
			return nil
		}
		return printJSON(result)
	case "search":
		query := flagValue(tail, "--query")
		if query == "" {
			query = flagValue(tail, "-q")
		}
		root := flagValue(tail, "--root")
		var result dale.FileSearchResult
		if err := remoteGET(baseURL, "/v1/fs/search", map[string]string{"q": query, "root": root}, token, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "run":
		sep := indexOf(tail, "--")
		if sep < 0 || sep == len(tail)-1 {
			return fmt.Errorf("remote run requires -- followed by command and args")
		}
		cmdArgs := tail[sep+1:]
		req := dale.ExecRequest{
			Command:        cmdArgs[0],
			Args:           cmdArgs[1:],
			Cwd:            flagValue(tail[:sep], "--cwd"),
			TimeoutSeconds: intFlag(tail[:sep], "--timeout", 30),
		}
		var result dale.ExecResult
		if err := remotePOST(baseURL, "/v1/exec", req, token, &result); err != nil {
			return err
		}
		return printJSON(result)
	default:
		printUsage()
		return nil
	}
}

func envCommand(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		printUsage()
		return nil
	}
	path := ".env"
	if _, ok := envfile.FindUp(".env"); ok {
		fmt.Println(".env already exists")
		return nil
	}
	token, err := randomHex(32)
	if err != nil {
		return err
	}
	content := "# Masterdale local secrets. Do not commit this file.\n\nDALE_TOKEN=" + token + "\nDALE_REMOTE_EXEC=0\nDALE_REMOTE_SCOPE=private\nLEARDALE_URL=http://127.0.0.1:7345\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	fmt.Println("created .env with a generated DALE_TOKEN")
	return nil
}

func up(cfg dale.Config, args []string) error {
	port := parsePort(args, 7345)
	restart := hasFlag(args, "--restart")
	listen := "0.0.0.0:" + strconv.Itoa(port)
	if err := ensureEnvForUp(); err != nil {
		return err
	}
	if ok, _ := localHealth(port); ok {
		if restart {
			pid, err := stopDaled(cfg)
			if err != nil {
				return err
			}
			fmt.Println("stopped daled pid", pid)
			waitForLocalStop(port, 5*time.Second)
		} else {
			fmt.Println("daled already running on", listen)
			return nil
		}
	}
	if ok, _ := localHealth(port); ok {
		fmt.Println("daled already running on", listen)
		return nil
	}
	if err := startBackgroundDaled(cfg, listen); err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := localHealth(port); ok {
			fmt.Println("daled running on", listen)
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("started daled, but health check did not answer; check %s", filepath.Join(cfg.DataDir, "daled.log"))
}

func down(cfg dale.Config) error {
	pid, err := stopDaled(cfg)
	if err != nil {
		return err
	}
	fmt.Println("stopped daled pid", pid)
	return nil
}

func stopDaled(cfg dale.Config) (int, error) {
	pidPath := filepath.Join(cfg.DataDir, "daled.pid")
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, err
	}
	if err := proc.Kill(); err != nil {
		return 0, err
	}
	_ = os.Remove(pidPath)
	return pid, nil
}

func waitForLocalStop(port int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok, _ := localHealth(port); !ok {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func status(args []string) error {
	port := parsePort(args, 7345)
	ok, detail := localHealth(port)
	if ok {
		fmt.Println("daled is running:", detail)
		return nil
	}
	fmt.Println("daled is not reachable:", detail)
	return nil
}

func ensureEnvForUp() error {
	path, ok := envfile.FindUp(".env")
	if !ok {
		token, err := randomHex(32)
		if err != nil {
			return err
		}
		content := "# Masterdale local secrets. Do not commit this file.\n\nDALE_TOKEN=" + token + "\nDALE_REMOTE_EXEC=1\nDALE_REMOTE_SCOPE=private\nLEARDALE_URL=http://127.0.0.1:7345\n"
		if err := os.WriteFile(".env", []byte(content), 0o600); err != nil {
			return err
		}
		_ = os.Setenv("DALE_TOKEN", token)
		_ = os.Setenv("DALE_REMOTE_EXEC", "1")
		_ = os.Setenv("DALE_REMOTE_SCOPE", "private")
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	if !strings.Contains(text, "DALE_TOKEN=") {
		token, err := randomHex(32)
		if err != nil {
			return err
		}
		text = appendEnvLine(text, "DALE_TOKEN="+token)
		_ = os.Setenv("DALE_TOKEN", token)
	}
	if !strings.Contains(text, "DALE_REMOTE_SCOPE=") {
		text = appendEnvLine(text, "DALE_REMOTE_SCOPE=private")
		_ = os.Setenv("DALE_REMOTE_SCOPE", "private")
	}
	text = setEnvLine(text, "DALE_REMOTE_EXEC", "1")
	_ = os.Setenv("DALE_REMOTE_EXEC", "1")
	return os.WriteFile(path, []byte(text), 0o600)
}

func startBackgroundDaled(cfg dale.Config, listen string) error {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	exe, err := managedDaemonExecutable(cfg)
	if err != nil {
		return err
	}
	logPath := filepath.Join(cfg.DataDir, "daled.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve", "--listen", listen)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	detachCommand(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = os.WriteFile(filepath.Join(cfg.DataDir, "daled.pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
	_ = logFile.Close()
	return nil
}

func managedDaemonExecutable(cfg dale.Config) (string, error) {
	source, err := os.Executable()
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(source)
	if ext == "" && strings.Contains(strings.ToLower(source), ".exe") {
		ext = ".exe"
	}
	dir := filepath.Join(cfg.DataDir, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "daled-"+strconv.FormatInt(time.Now().UnixNano(), 10)+ext)
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dest, nil
}

func setEnvLine(text string, key string, value string) string {
	line := key + "=" + value
	lines := strings.Split(text, "\n")
	found := false
	for i, existing := range lines {
		trimmed := strings.TrimSpace(existing)
		if strings.HasPrefix(trimmed, key+"=") || strings.HasPrefix(trimmed, "export "+key+"=") {
			lines[i] = line
			found = true
		}
	}
	text = strings.Join(lines, "\n")
	if !found {
		text = appendEnvLine(text, line)
	}
	return text
}

func appendEnvLine(text string, line string) string {
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + line + "\n"
}

func localHealth(port int) (bool, string) {
	client := http.Client{Timeout: 700 * time.Millisecond}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	resp, err := client.Get(url)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false, resp.Status
	}
	return true, url
}

func chat(cfg dale.Config, store *dale.Store, args []string) error {
	if len(args) == 0 {
		events, err := store.List(20)
		if err != nil {
			return err
		}
		for _, e := range events {
			if e.Channel == "chat" {
				fmt.Printf("%s %-8s %s\n", e.Timestamp, e.Actor, dale.TextFromBody(e.Body))
			}
		}
		return nil
	}
	e, err := dale.NewEvent(cfg.DeviceID, "user", "chat", "message.created", map[string]any{"text": strings.Join(args, " ")}, nil)
	if err != nil {
		return err
	}
	if err := store.Append(e); err != nil {
		return err
	}
	fmt.Println(e.ID)
	return nil
}

func search(store *dale.Store, query string) error {
	results, err := store.Search(query, 25)
	if err != nil {
		return err
	}
	for _, r := range results {
		fmt.Printf("%s %-10s %-24s %s\n", r.Timestamp, r.Channel, r.Kind, r.Snippet)
	}
	return nil
}

func ask(cfg dale.Config, store *dale.Store, args []string) error {
	question := strings.TrimSpace(strings.Join(questionArgs(args), " "))
	if question == "" {
		return fmt.Errorf("ask requires a question")
	}
	role := flagValue(args, "--role")
	explicitRole := role != ""
	if !explicitRole {
		role = models.RoleContext
	}
	if hasFlag(args, "--fast") {
		role = models.RoleFast
	}
	if hasFlag(args, "--deep") {
		role = models.RoleReasoning
	}
	model := flagValue(args, "--model")
	if model == "" && explicitRole {
		model = cfg.Models.RoleModel(role)
	}
	timeout := intFlag(args, "--timeout", 300)
	maxTokens := intFlag(args, "--max-tokens", 700)
	prompt := "You are Masterdale's local AI operator. Answer the user's question using the repository context below. Be direct. If the context is insufficient, say what local command or file should be inspected next.\n\nQuestion:\n" + question + "\n\nRepository context:\n" + repoAskContext(role, question)
	userEvent, err := dale.NewEvent(cfg.DeviceID, "user", "chat", "message.created", map[string]any{
		"text": question,
		"mode": "ask",
		"role": role,
	}, nil)
	if err != nil {
		return err
	}
	if err := store.Append(userEvent); err != nil {
		return err
	}
	think := false
	resp, err := dale.CompleteWithOllama(context.Background(), cfg, dale.CompleteRequest{
		Model:          model,
		Role:           role,
		Prompt:         prompt,
		TimeoutSeconds: timeout,
		MaxTokens:      maxTokens,
		Think:          &think,
		PlainText:      true,
		KeepAlive:      "15m",
	})
	if err != nil {
		return err
	}
	answer := strings.TrimSpace(resp.Response)
	if answer == "" {
		return fmt.Errorf("model %s returned an empty answer", resp.Model)
	}
	agentEvent, err := dale.NewEvent(cfg.DeviceID, "agent", "chat", "message.created", map[string]any{
		"text":  answer,
		"mode":  "ask",
		"role":  role,
		"model": resp.Model,
	}, nil)
	if err != nil {
		return err
	}
	if err := store.Append(agentEvent); err != nil {
		return err
	}
	fmt.Printf("model: %s\n\n%s\n", resp.Model, answer)
	return nil
}

func questionArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "--role", "--timeout", "--max-tokens":
			i++
		case "--fast", "--deep":
			continue
		default:
			out = append(out, args[i])
		}
	}
	return out
}

func repoAskContext(role string, question string) string {
	paths := askContextPaths(question)
	perFileLimit := 1600
	totalLimit := 7000
	if role == models.RoleFast {
		perFileLimit = 1000
		totalLimit = 4400
	} else if role == models.RoleReasoning {
		perFileLimit = 2200
		totalLimit = 9000
	}
	var sections []string
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sections = append(sections, "## "+path+"\n"+dale.Shorten(string(b), perFileLimit))
	}
	return dale.Shorten(strings.Join(sections, "\n\n"), totalLimit)
}

func askContextPaths(question string) []string {
	q := strings.ToLower(question)
	paths := []string{"README.md"}
	add := func(path string) {
		for _, existing := range paths {
			if existing == path {
				return
			}
		}
		paths = append(paths, path)
	}
	if strings.Contains(q, "architect") || strings.Contains(q, "organized") || strings.Contains(q, "structure") || strings.Contains(q, "infra") {
		add("docs/architecture.md")
	}
	if strings.Contains(q, "security") || strings.Contains(q, "safe") || strings.Contains(q, "token") || strings.Contains(q, "auth") || strings.Contains(q, "public") {
		add("docs/security.md")
	}
	if strings.Contains(q, "command") || strings.Contains(q, "run") || strings.Contains(q, "setup") || strings.Contains(q, "start") || strings.Contains(q, "monitor") {
		add("docs/operations.md")
	}
	if strings.Contains(q, "model") || strings.Contains(q, "ollama") || strings.Contains(q, "gemma") || strings.Contains(q, "qwen") || strings.Contains(q, "ministral") {
		add("docs/local-models.md")
	}
	if strings.Contains(q, "vision") || strings.Contains(q, "portfolio") {
		add("docs/vision.md")
	}
	if len(paths) == 1 {
		add("docs/architecture.md")
		add("docs/operations.md")
	}
	return paths
}

func benchModels(cfg dale.Config) error {
	models := []string{cfg.Models.Primary, cfg.Models.Fast, cfg.Models.Text, cfg.Models.Context, cfg.Models.Structured, cfg.Models.Reasoning, cfg.Models.Vision}
	models = append(models, cfg.Models.Fallbacks...)
	seen := map[string]bool{}
	for _, model := range models {
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		start := time.Now()
		think := false
		resp, err := dale.CompleteWithOllama(context.Background(), cfg, dale.CompleteRequest{
			Model:          model,
			Prompt:         `Return exactly {"ok":true,"model":"` + model + `"}.`,
			TimeoutSeconds: 180,
			MaxTokens:      64,
			Think:          &think,
			KeepAlive:      "15m",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ok":    map[string]any{"type": "boolean"},
					"model": map[string]any{"type": "string"},
				},
				"required": []string{"ok", "model"},
			},
		})
		status := "ok"
		if err != nil || !resp.Valid {
			status = "fail"
		}
		fmt.Printf("%-20s %-5s %s %s\n", model, status, time.Since(start).Round(time.Millisecond), resp.Error)
	}
	return nil
}

func devices() error {
	devices, err := dale.TailscaleDevices(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("%-24s %-8s %-7s %s\n", "HOST", "OS", "ONLINE", "IP")
	for _, device := range devices {
		fmt.Printf("%-24s %-8s %-7s %s\n", device.HostName, device.OS, strconv.FormatBool(device.Online || device.Active), firstIP(device.IPs))
	}
	return nil
}

func fleet(cfg dale.Config, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	port := 7345
	switch args[0] {
	case "devices":
		return devices()
	case "probe":
		port = parsePort(args[1:], port)
		probes, err := dale.ProbeFleet(context.Background(), port)
		if err != nil {
			return err
		}
		return printJSON(probes)
	case "doctor":
		return fleetDoctor(cfg, args[1:])
	case "npm-scan":
		port = parsePort(args[1:], port)
		packages, roots := parseScanArgs(args[1:])
		local := dale.ScanNPM(defaultRoots(cfg, roots), packages, 5000)
		type remoteResult struct {
			Device string             `json:"device"`
			URL    string             `json:"url"`
			OK     bool               `json:"ok"`
			Result dale.NPMScanResult `json:"result,omitempty"`
			Error  string             `json:"error,omitempty"`
		}
		var remotes []remoteResult
		probes, err := dale.ProbeFleet(context.Background(), port)
		if err != nil {
			return err
		}
		token := os.Getenv("DALE_TOKEN")
		if token == "" {
			token = cfg.AccessToken
		}
		for _, probe := range probes {
			if !probe.OK || strings.Contains(probe.URL, "127.0.0.1") {
				continue
			}
			url := strings.TrimSuffix(probe.URL, "/healthz") + "/v1/scans/npm?" + scanQuery(packages)
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			item := remoteResult{Device: probe.Device.HostName, URL: url}
			if err != nil {
				item.Error = err.Error()
			} else {
				item.OK = resp.StatusCode < 300
				if item.OK {
					_ = json.NewDecoder(resp.Body).Decode(&item.Result)
				} else {
					item.Error = resp.Status
				}
				_ = resp.Body.Close()
			}
			remotes = append(remotes, item)
		}
		return printJSON(map[string]any{"local": local, "remote": remotes})
	case "git-audit":
		return fleetGitAudit(cfg, args[1:])
	}
	printUsage()
	return nil
}

func gitCommand(cfg dale.Config, args []string) error {
	if len(args) == 0 || args[0] != "audit" {
		printUsage()
		return nil
	}
	root := flagValue(args[1:], "--root")
	if root == "" {
		root = cfg.SafeRoots[0]
	}
	fetch := hasFlag(args[1:], "--fetch")
	return printJSON(dale.AuditGitRepos(root, fetch))
}

func fleetGitAudit(cfg dale.Config, args []string) error {
	port := parsePort(args, 7345)
	fetch := hasFlag(args, "--fetch")
	deviceName := flagValue(args, "--device")
	type remoteAudit struct {
		Device string        `json:"device"`
		URL    string        `json:"url"`
		OK     bool          `json:"ok"`
		Audit  dale.GitAudit `json:"audit,omitempty"`
		Error  string        `json:"error,omitempty"`
	}
	var remotes []remoteAudit
	var probes []dale.FleetProbe
	if deviceName != "" {
		device, err := dale.FindFleetDevice(context.Background(), deviceName)
		if err != nil {
			return err
		}
		probes = []dale.FleetProbe{dale.ProbeDevice(context.Background(), device, port)}
	} else {
		var err error
		probes, err = dale.ProbeFleet(context.Background(), port)
		if err != nil {
			return err
		}
	}
	for _, probe := range probes {
		if !probe.OK {
			remotes = append(remotes, remoteAudit{Device: probe.Device.HostName, URL: strings.TrimSuffix(probe.URL, "/healthz"), Error: probe.Error})
			continue
		}
		baseURL := strings.TrimSuffix(probe.URL, "/healthz")
		params := map[string]string{}
		if fetch {
			params["fetch"] = "1"
		}
		var audit dale.GitAudit
		err := remoteGET(baseURL, "/v1/git/audit", params, cfg.AccessToken, &audit)
		item := remoteAudit{Device: probe.Device.HostName, URL: baseURL, OK: err == nil, Audit: audit}
		if err != nil {
			item.Error = err.Error()
		}
		remotes = append(remotes, item)
	}
	return printJSON(map[string]any{
		"local":  dale.AuditGitRepos(cfg.SafeRoots[0], fetch),
		"remote": remotes,
	})
}

func firstIP(ips []string) string {
	for _, ip := range ips {
		if strings.Count(ip, ".") == 3 {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

func npmScan(cfg dale.Config, args []string) error {
	packages, roots := parseScanArgs(args)
	return printJSON(dale.ScanNPM(defaultRoots(cfg, roots), packages, 5000))
}

func parseScanArgs(args []string) ([]string, []string) {
	var packages, roots []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--package", "-p":
			if i+1 < len(args) {
				packages = append(packages, args[i+1])
				i++
			}
		case "--root":
			if i+1 < len(args) {
				roots = append(roots, args[i+1])
				i++
			}
		}
	}
	return packages, roots
}

func defaultRoots(cfg dale.Config, roots []string) []string {
	if len(roots) > 0 {
		return roots
	}
	return cfg.SafeRoots
}

func parsePort(args []string, fallback int) int {
	for i := 0; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			if port, err := strconv.Atoi(args[i+1]); err == nil {
				return port
			}
		}
	}
	return fallback
}

func scanQuery(packages []string) string {
	values := url.Values{}
	for _, pkg := range packages {
		values.Add("package", pkg)
	}
	return values.Encode()
}

func remoteBaseURL(args []string, port int) (string, error) {
	host := flagValue(args, "--host")
	if host != "" {
		if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
			return strings.TrimRight(host, "/"), nil
		}
		return "http://" + host + ":" + strconv.Itoa(port), nil
	}
	device := flagValue(args, "--device")
	if device == "" {
		probes, err := dale.ProbeFleet(context.Background(), port)
		if err != nil {
			return "", err
		}
		var candidates []dale.FleetProbe
		for _, probe := range probes {
			if probe.OK {
				candidates = append(candidates, probe)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no reachable fleet device found")
		}
		if len(candidates) > 1 {
			return "", fmt.Errorf("multiple reachable devices; pass --device")
		}
		return strings.TrimSuffix(candidates[0].URL, "/healthz"), nil
	}
	found, err := dale.FindFleetDevice(context.Background(), device)
	if err != nil {
		return "", err
	}
	probe := dale.ProbeDevice(context.Background(), found, port)
	if !probe.OK {
		return "", fmt.Errorf("device %q is not reachable: %s", device, probe.Error)
	}
	return strings.TrimSuffix(probe.URL, "/healthz"), nil
}

func remoteGET(baseURL string, path string, params map[string]string, token string, out any) error {
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
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return doJSON(req, out)
}

func remotePOST(baseURL string, path string, body any, token string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return doJSON(req, out)
}

func doJSON(req *http.Request, out any) error {
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		if msg, ok := payload["error"].(string); ok && msg != "" {
			return fmt.Errorf("%s: %s", resp.Status, msg)
		}
		return fmt.Errorf(resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type doctorReport struct {
	Device     string        `json:"device"`
	URL        string        `json:"url,omitempty"`
	Checks     []doctorCheck `json:"checks"`
	Conclusion string        `json:"conclusion"`
}

func fleetDoctor(cfg dale.Config, args []string) error {
	port := parsePort(args, 7345)
	deviceName := flagValue(args, "--device")
	if deviceName == "" {
		deviceName = firstPositional(args)
	}
	report := doctorReport{Device: deviceName}
	var probe dale.FleetProbe
	if deviceName == "" {
		probes, err := dale.ProbeFleet(context.Background(), port)
		if err != nil {
			report.Checks = append(report.Checks, doctorCheck{Name: "tailscale", Detail: err.Error()})
			report.Conclusion = "Could not inspect Tailnet devices."
			return printJSON(report)
		}
		for _, candidate := range probes {
			if candidate.OK {
				if probe.OK {
					report.Checks = append(report.Checks, doctorCheck{Name: "healthz", Detail: "multiple reachable devices; pass --device"})
					report.Conclusion = "More than one device is reachable. Run `dale fleet doctor --device <name>`."
					return printJSON(report)
				}
				probe = candidate
			}
		}
		if !probe.OK {
			report.Checks = append(report.Checks, doctorCheck{Name: "healthz", Detail: "no reachable daled agents"})
			report.Conclusion = "No devices are currently reachable on the selected port."
			return printJSON(report)
		}
		report.Device = probe.Device.HostName
	} else {
		device, err := dale.FindFleetDevice(context.Background(), deviceName)
		if err != nil {
			report.Checks = append(report.Checks, doctorCheck{Name: "tailscale-device", Detail: err.Error()})
			report.Conclusion = "Device is not visible in tailscale status."
			return printJSON(report)
		}
		report.Checks = append(report.Checks, doctorCheck{Name: "tailscale-device", OK: true, Detail: device.OS})
		probe = dale.ProbeDevice(context.Background(), device, port)
	}
	if !probe.OK && isSelfDevice(probe.Device) {
		report.Checks = append(report.Checks, doctorCheck{Name: "auto-start", Detail: "local device detected; starting daled"})
		if err := up(cfg, []string{"--port", strconv.Itoa(port)}); err != nil {
			report.Checks = append(report.Checks, doctorCheck{Name: "auto-start", Detail: err.Error()})
		} else {
			probe = dale.ProbeDevice(context.Background(), probe.Device, port)
			if probe.OK {
				report.Checks = append(report.Checks, doctorCheck{Name: "auto-start", OK: true, Detail: "daled started"})
			}
		}
	}
	report.URL = strings.TrimSuffix(probe.URL, "/healthz")
	if !probe.OK {
		report.Checks = append(report.Checks, doctorCheck{Name: "healthz", Detail: probe.Error})
		report.Conclusion = "Device is visible on Tailscale, but daled is not reachable on the selected port. Start/restart `go run ./cmd/dale serve --listen 0.0.0.0:7345` on that device and check firewall permissions."
		return printJSON(report)
	}
	report.Checks = append(report.Checks, doctorCheck{Name: "healthz", OK: true, Detail: probe.URL})
	var node map[string]any
	if err := remoteGET(report.URL, "/v1/node/report", nil, cfg.AccessToken, &node); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "auth", Detail: err.Error()})
		report.Conclusion = "The agent is reachable, but authenticated endpoints failed. Put the same DALE_TOKEN in `.env` on both devices and restart daled."
		return printJSON(report)
	}
	report.Checks = append(report.Checks, doctorCheck{Name: "auth", OK: true, Detail: "token accepted"})
	var entries []dale.FileEntry
	if err := remoteGET(report.URL, "/v1/fs/list", nil, cfg.AccessToken, &entries); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "file-list", Detail: err.Error()})
		report.Conclusion = "Auth works, but this agent does not expose generic file APIs. Pull latest Masterdale on the remote device and restart daled."
		return printJSON(report)
	}
	report.Checks = append(report.Checks, doctorCheck{Name: "file-list", OK: true, Detail: fmt.Sprintf("%d entries", len(entries))})
	var search dale.FileSearchResult
	searchOK := false
	if err := remoteGET(report.URL, "/v1/fs/search", map[string]string{"q": "README", "max_files": "250", "max_matches": "10"}, cfg.AccessToken, &search); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "file-search", Detail: err.Error()})
	} else {
		searchOK = true
		report.Checks = append(report.Checks, doctorCheck{Name: "file-search", OK: true, Detail: fmt.Sprintf("%d matches", len(search.Matches))})
	}
	command, argsForCommand := doctorCommand(probe.Device.OS)
	var execResult dale.ExecResult
	if err := remotePOST(report.URL, "/v1/exec", dale.ExecRequest{Command: command, Args: argsForCommand, TimeoutSeconds: 10}, cfg.AccessToken, &execResult); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "remote-exec", Detail: err.Error()})
		report.Conclusion = "File operations work. Remote command execution is disabled or blocked; set DALE_REMOTE_EXEC=1 in `.env` on the remote device and restart daled if you want SSH-like commands."
		return printJSON(report)
	}
	report.Checks = append(report.Checks, doctorCheck{Name: "remote-exec", OK: execResult.ExitCode == 0, Detail: strings.TrimSpace(execResult.Stdout + execResult.Stderr)})
	if !searchOK {
		report.Conclusion = "Health, auth, file list, and remote command execution work. File search timed out or failed; try a narrower root/query or pull the latest agent."
		return printJSON(report)
	}
	report.Conclusion = "Device is ready for SSH-like Masterdale access: health, auth, file list/search, and remote command execution all work."
	return printJSON(report)
}

func doctorCommand(osName string) (string, []string) {
	if strings.EqualFold(osName, "windows") {
		return "cmd", []string{"/C", "ver"}
	}
	return "uname", []string{"-a"}
}

func isSelfDevice(device dale.FleetDevice) bool {
	host, _ := os.Hostname()
	return strings.EqualFold(device.HostName, host) || strings.EqualFold(strings.TrimSuffix(device.DNSName, "."), strings.TrimSuffix(host, "."))
}

func flagValue(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func intFlag(args []string, name string, fallback int) int {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			if value, err := strconv.Atoi(args[i+1]); err == nil {
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

func indexOf(args []string, value string) int {
	for i, arg := range args {
		if arg == value {
			return i
		}
	}
	return -1
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func printUsage() {
	fmt.Println(`dale commands:
  init
  env init
  up [--restart] [--port 7345]
  serve [--listen 127.0.0.1:7345]
  chat [message]
  ask [--fast|--deep] [--role context|text|fast|reasoning|structured] [--model name] [--timeout seconds] [--max-tokens n] <question>
  context search <query>
  sessions ingest
  models bench
  git audit [--fetch] [--root path]
  npm scan --package <name> [--root path]
  devices
  fleet devices
  fleet probe [--port 7345]
  fleet doctor [--device name] [--port 7345]
  fleet git-audit [--device name] [--fetch] [--port 7345]
  fleet npm-scan --package <name> [--port 7345]
  remote list [--device name] [path]
  remote read [--device name] <path>
  remote search [--device name] --query text [--root path]
  remote run [--device name] [--cwd path] [--timeout seconds] -- command [args...]
  token`)
}
