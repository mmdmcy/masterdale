package dale

import (
	"context"
	"runtime"
	"testing"
)

func TestRunCommand(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SafeRoots = []string{t.TempDir()}
	req := ExecRequest{Command: "echo", Args: []string{"hello"}}
	if runtime.GOOS == "windows" {
		req = ExecRequest{Command: "cmd", Args: []string{"/C", "echo hello"}}
	}
	result, err := RunCommand(context.Background(), cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("unexpected exit: %#v", result)
	}
	if result.Stdout == "" {
		t.Fatalf("expected stdout: %#v", result)
	}
}
