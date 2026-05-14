package autodale

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type CheckResult struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Details map[string]any `json:"details"`
}

func SelfhostCheck(ctx context.Context) []CheckResult {
	var out []CheckResult
	out = append(out, systemCheck())
	out = append(out, commandCheck(ctx, "ollama", "ollama", "list"))
	out = append(out, commandCheck(ctx, "tailscale", "tailscale", "status", "--json"))
	out = append(out, commandCheck(ctx, "docker", "docker", "ps"))
	return out
}

func SSHCheck(ctx context.Context, name string, host string, port int) CheckResult {
	if host == "" {
		return CheckResult{Name: name, Status: "missing", Summary: "host is required", Details: map[string]any{"port": port}}
	}
	if port == 0 {
		port = 22
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return CheckResult{Name: name, Status: "fail", Summary: err.Error(), Details: map[string]any{"host": host, "port": port}}
	}
	_ = conn.Close()
	return CheckResult{Name: name, Status: "ok", Summary: "tcp reachable", Details: map[string]any{"host": host, "port": port}}
}

func systemCheck() CheckResult {
	details := map[string]any{
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
		"cpus":     runtime.NumCPU(),
		"hostname": hostname(),
	}
	if runtime.GOOS == "linux" {
		if mem := readMeminfo(); len(mem) > 0 {
			details["mem_total_kb"] = mem["MemTotal"]
			details["mem_available_kb"] = mem["MemAvailable"]
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs("/", &st); err == nil {
			total := st.Blocks * uint64(st.Bsize)
			free := st.Bavail * uint64(st.Bsize)
			details["disk_total_bytes"] = total
			details["disk_free_bytes"] = free
		}
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			details["loadavg"] = strings.TrimSpace(string(b))
		}
	}
	return CheckResult{Name: "system", Status: "ok", Summary: "read-only local system snapshot", Details: details}
}

func commandCheck(ctx context.Context, name string, command string, args ...string) CheckResult {
	if _, err := exec.LookPath(command); err != nil {
		return CheckResult{Name: name, Status: "missing", Summary: command + " not found", Details: map[string]any{"command": command}}
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, command, args...)
	out, err := cmd.CombinedOutput()
	if cctx.Err() != nil {
		return CheckResult{Name: name, Status: "timeout", Summary: cctx.Err().Error(), Details: map[string]any{"command": command}}
	}
	if err != nil {
		return CheckResult{Name: name, Status: "warn", Summary: err.Error(), Details: map[string]any{"output": shorten(string(out), 800)}}
	}
	if name == "tailscale" {
		return CheckResult{Name: name, Status: "ok", Summary: "available", Details: map[string]any{"output": "redacted tailscale status; run tailscale status locally for full device details"}}
	}
	return CheckResult{Name: name, Status: "ok", Summary: "available", Details: map[string]any{"output": shorten(string(out), 800)}}
}

func readMeminfo() map[string]uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]uint64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		out[key] = value
	}
	return out
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
