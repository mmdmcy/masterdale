package autodale

import (
	"context"
	"testing"
)

func TestSelfhostCheckReturnsSystem(t *testing.T) {
	results := SelfhostCheck(context.Background())
	if len(results) == 0 || results[0].Name != "system" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestSSHCheckRequiresHost(t *testing.T) {
	result := SSHCheck(context.Background(), "ssh", "", 22)
	if result.Status != "missing" {
		t.Fatalf("expected missing host result: %#v", result)
	}
}
