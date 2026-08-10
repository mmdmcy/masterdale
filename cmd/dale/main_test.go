package main

import "testing"

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
