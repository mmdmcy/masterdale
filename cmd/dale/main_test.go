package main

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultRemoteURL(t *testing.T) {
	t.Setenv("LEARDALE_URL", "http://100.64.0.1:7345/")
	if got := defaultRemoteURL(); got != "http://100.64.0.1:7345" {
		t.Fatalf("unexpected legacy url: %q", got)
	}

	t.Setenv("DALE_URL", "http://100.64.0.2:7345/")
	if got := defaultRemoteURL(); got != "http://100.64.0.2:7345" {
		t.Fatalf("expected DALE_URL to win: %q", got)
	}

	t.Setenv("MASTERDALE_URL", "http://100.64.0.3:7345/")
	if got := defaultRemoteURL(); got != "http://100.64.0.3:7345" {
		t.Fatalf("expected MASTERDALE_URL to win: %q", got)
	}
}

func TestShouldOverrideEnvFile(t *testing.T) {
	for _, command := range []string{"up", "remote", "fleet", "token"} {
		if !shouldOverrideEnvFile([]string{command}) {
			t.Fatalf("expected %q to use the canonical environment file", command)
		}
	}
	for _, args := range [][]string{nil, {"serve"}, {"status"}} {
		if shouldOverrideEnvFile(args) {
			t.Fatalf("did not expect %q to override inherited environment", args)
		}
	}
}

func TestDaleUpKeepsRemoteExecOffUnlessAsked(t *testing.T) {
	t.Setenv("DALE_REMOTE_EXEC", "")
	t.Setenv("DALE_TOKEN", "")
	t.Chdir(t.TempDir())
	if err := ensureEnvForUp(false); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(".env")
	if !strings.Contains(string(env), "\nDALE_REMOTE_EXEC=0\n") || os.Getenv("DALE_REMOTE_EXEC") != "0" {
		t.Fatalf("new .env should keep remote exec off: %s", env)
	}

	// An existing choice is left alone; --exec turns it on.
	if err := os.WriteFile(".env", []byte("DALE_TOKEN=x\nDALE_REMOTE_EXEC=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureEnvForUp(false); err != nil {
		t.Fatal(err)
	}
	if env, _ = os.ReadFile(".env"); !strings.Contains(string(env), "DALE_REMOTE_EXEC=1\n") {
		t.Fatalf("existing setting changed: %s", env)
	}
	if err := os.WriteFile(".env", []byte("DALE_TOKEN=x\nDALE_REMOTE_EXEC=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureEnvForUp(true); err != nil {
		t.Fatal(err)
	}
	if env, _ = os.ReadFile(".env"); !strings.Contains(string(env), "DALE_REMOTE_EXEC=1\n") || os.Getenv("DALE_REMOTE_EXEC") != "1" {
		t.Fatalf("--exec should turn remote exec on: %s", env)
	}
}
