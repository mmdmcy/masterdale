package dale

import (
	"bytes"
	"context"
	"errors"
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
}

func RemoteExecEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DALE_REMOTE_EXEC")))
	return v == "1" || v == "true" || v == "yes"
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
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
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
		Stdout:     Shorten(stdout.String(), 64*1024),
		Stderr:     Shorten(stderr.String(), 64*1024),
		DurationMS: time.Since(start).Milliseconds(),
	}
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
