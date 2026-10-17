package dale

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type ExecRequest struct {
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type ExecResult struct {
	Command    string   `json:"command"`
	Args       []string `json:"args,omitempty"`
	Cwd        string   `json:"cwd"`
	ExitCode   int      `json:"exit_code"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	DurationMS int64    `json:"duration_ms"`
	Error      string   `json:"error,omitempty"`
	// Notes says plainly when output was cut or the timeout was lowered,
	// so nothing is lost silently.
	Notes []string `json:"notes,omitempty"`
}

const (
	// execOutputLimit is per stream; output beyond it is cut and noted.
	execOutputLimit = 8 << 20
	// execMaxTimeout is the longest a remote command may run.
	execMaxTimeout = 30 * time.Minute
)

func RemoteExecEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DALE_REMOTE_EXEC")))
	return v == "1" || v == "true" || v == "yes"
}

// capOutput keeps a stream whole up to execOutputLimit and notes a cut.
func capOutput(text, name string, notes *[]string) string {
	if len(text) <= execOutputLimit {
		return text
	}
	*notes = append(*notes, fmt.Sprintf("%s cut at %d of %d bytes", name, execOutputLimit, len(text)))
	return text[:execOutputLimit]
}

func RunCommand(ctx context.Context, cfg Config, req ExecRequest) (ExecResult, error) {
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" {
		return ExecResult{}, errors.New("command is required")
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var notes []string
	if timeout > execMaxTimeout {
		notes = append(notes, fmt.Sprintf("timeout lowered from %s to the maximum %s", timeout, execMaxTimeout))
		timeout = execMaxTimeout
	}
	cwd, err := safePath(cfg.SafeRoots, req.Cwd)
	if err != nil {
		return ExecResult{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(cctx, req.Command, req.Args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result := ExecResult{
		Command:    req.Command,
		Args:       req.Args,
		Cwd:        cwd,
		Stdout:     capOutput(stdout.String(), "stdout", &notes),
		Stderr:     capOutput(stderr.String(), "stderr", &notes),
		DurationMS: time.Since(start).Milliseconds(),
	}
	result.Notes = notes
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if cctx.Err() != nil {
		result.Error = cctx.Err().Error()
		return result, nil
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result, nil
}
