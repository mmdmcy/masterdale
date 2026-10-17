package dale

import (
	"context"
	"runtime"
	"strings"
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

func TestCapOutputNotesACut(t *testing.T) {
	var notes []string
	if got := capOutput("short", "stdout", &notes); got != "short" || len(notes) != 0 {
		t.Fatalf("short output changed: %q %v", got, notes)
	}
	long := strings.Repeat("x", execOutputLimit+10)
	if got := capOutput(long, "stdout", &notes); len(got) != execOutputLimit || len(notes) != 1 {
		t.Fatalf("long output: %d bytes, notes %v", len(got), notes)
	}
}
