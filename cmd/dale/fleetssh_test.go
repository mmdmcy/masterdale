package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmdmcy/masterdale/internal/dale"
)

func TestMarshalOpenSSHPrivateKeyIsReadableBySSHKeygen(t *testing.T) {
	pub, priv, err := generateEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	comment := "sleep@server-fleet"
	pemBytes := encodeOpenSSHPrivateKeyPEM(pub, priv, comment)

	path := filepath.Join(t.TempDir(), "unit_fleet_ed25519")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not available for round-trip verification")
	}
	out, err := exec.Command(sshKeygen, "-y", "-f", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen rejected the generated key: %v: %s", err, strings.TrimSpace(string(out)))
	}
	line := strings.TrimSpace(string(out))
	want := publicKeyLine(pub, comment)
	if line != want {
		t.Fatalf("public key mismatch:\n got: %s\nwant: %s", line, want)
	}
}

func TestSSHConfigLinesHaveAlias(t *testing.T) {
	config := "Host mini\n  HostName 100.64.0.10\n\nHost windowspc desktop-a34ht70\n  User homer\n"
	for _, alias := range []string{"mini", "windowspc", "DESKTOP-A34HT70"} {
		if !sshConfigLinesHaveAlias(config, alias) {
			t.Fatalf("expected alias %q to be present", alias)
		}
	}
	for _, alias := range []string{"laptop", "a34ht70", "mini-nested"} {
		if sshConfigLinesHaveAlias(config, alias) {
			t.Fatalf("expected alias %q to be absent", alias)
		}
	}
}

func TestRemoteRunWaitsAsLongAsItsTimeout(t *testing.T) {
	if got := requestTimeout(dale.ExecRequest{TimeoutSeconds: 900}); got != 930*time.Second {
		t.Fatalf("900 s command: client waits %v", got)
	}
	if got := requestTimeout(dale.ExecRequest{TimeoutSeconds: 10}); got != 2*time.Minute {
		t.Fatalf("short command: client waits %v", got)
	}
}
