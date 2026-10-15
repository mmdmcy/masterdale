package dale

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func linuxmiceTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DeviceID = "test-device"
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg, store)
}

func getLinuxMice(t *testing.T, server *Server, path string) (int, LinuxMiceView) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	var view LinuxMiceView
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode view: %v: %s", err, w.Body.String())
		}
	}
	return w.Code, view
}

func TestLinuxMiceViewsPassJSONThroughAndValidateInput(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "lmice")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprintf '{\"schema_version\":\"linuxmice.workspace.map.v1\",\"ok\":true}'\nexit 4\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DALE_LMICE", fake)
	server := linuxmiceTestServer(t)

	code, view := getLinuxMice(t, server, "/v1/linuxmice/map")
	if code != http.StatusOK || !view.Available || view.Error != "" {
		t.Fatalf("map view: %d %+v", code, view)
	}
	if !strings.Contains(string(view.Data), "linuxmice.workspace.map.v1") {
		t.Fatalf("data not passed through: %s", view.Data)
	}
	args, _ := os.ReadFile(argsFile)
	if got := strings.Fields(string(args)); strings.Join(got, " ") != "workspace map --format json" {
		t.Fatalf("unexpected lmice arguments: %q", got)
	}

	code, _ = getLinuxMice(t, server, "/v1/linuxmice/diff?location=~/Documents/x&a=nitori&b=lawnmower")
	if code != http.StatusOK {
		t.Fatalf("diff status %d", code)
	}
	args, _ = os.ReadFile(argsFile)
	if got := strings.Join(strings.Fields(string(args)), " "); got != "workspace diff ~/Documents/x --between nitori lawnmower --format json" {
		t.Fatalf("unexpected diff arguments: %q", got)
	}

	for _, path := range []string{
		"/v1/linuxmice/diff?location=/etc/passwd&a=nitori&b=lawnmower",
		"/v1/linuxmice/diff?location=~/x&a=--execute&b=lawnmower",
		"/v1/linuxmice/diff?location=~/x&a=a;rm&b=b",
	} {
		if code, _ := getLinuxMice(t, server, path); code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", path, code)
		}
	}
	if code, _ := getLinuxMice(t, server, "/v1/linuxmice/carry"); code != http.StatusNotFound {
		t.Fatalf("unknown view: want 404, got %d", code)
	}
}

func TestLinuxMiceViewWithoutBinaryIsUnavailable(t *testing.T) {
	// No lmice anywhere this process could find one, so nothing real runs.
	t.Setenv("DALE_LMICE", "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if LinuxMiceBinary() != "" && LinuxMiceBinary() != "/usr/local/bin/lmice" && LinuxMiceBinary() != "/usr/bin/lmice" {
		t.Fatalf("unexpected lmice: %s", LinuxMiceBinary())
	}
	if LinuxMiceBinary() != "" {
		t.Skip("a system-wide lmice is installed")
	}
	view := RunLinuxMiceView(t.Context(), "map", linuxmiceViews["map"], linuxmiceTimeout)
	if view.Available || view.Hint == "" {
		t.Fatalf("missing lmice should be reported as unavailable with a hint: %+v", view)
	}
}

func postJob(t *testing.T, server *Server, body string, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/linuxmice/jobs", bytes.NewBufferString(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	return w
}

func TestLinuxMiceJobsPlanFreelyAndChangeOnlyWhenAllowed(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "lmice")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprintf '{\"schema_version\":\"linuxmice.workspace.sync.v1\"}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DALE_LMICE", fake)
	t.Setenv("DALE_LINUXMICE_ACTIONS", "")
	server := linuxmiceTestServer(t)

	w := postJob(t, server, `{"kind":"plan-sync","a":"nitori","b":"lawnmower"}`, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("plan job: %d %s", w.Code, w.Body.String())
	}
	var job LinuxMiceJob
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	if job.Changes || job.Command != "lmice workspace sync nitori lawnmower --format json" {
		t.Fatalf("unexpected plan job: %+v", job)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, _ := server.linuxmice.job(job.ID)
		if current.State != "running" {
			if current.State != "done" || !strings.Contains(string(current.View.Data), "sync.v1") {
				t.Fatalf("plan job finished badly: %+v", current)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plan job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if w := postJob(t, server, `{"kind":"execute-sync","a":"nitori","b":"lawnmower"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("execute without DALE_LINUXMICE_ACTIONS: want 403, got %d", w.Code)
	}
	if w := postJob(t, server, `{"kind":"plan-sync","a":"nitori","b":"lawnmower"}`, "https://evil.example"); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin job: want 403, got %d", w.Code)
	}
	for _, body := range []string{
		`{"kind":"plan-sync","a":"nitori","b":"nitori"}`,
		`{"kind":"plan-sync","a":"-x","b":"lawnmower"}`,
		`{"kind":"integration-enable","id":"Bad;id","device":"nitori"}`,
		`{"kind":"agents-start","device":"-x"}`,
		`{"kind":"rm"}`,
	} {
		if w := postJob(t, server, body, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", body, w.Code)
		}
	}

	t.Setenv("DALE_LINUXMICE_ACTIONS", "1")
	w = postJob(t, server, `{"kind":"integration-disable","id":"gitdcy","device":"lawnmower"}`, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("allowed change: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	if !job.Changes || job.Command != "lmice integrations disable gitdcy --device lawnmower --format json" {
		t.Fatalf("unexpected change job: %+v", job)
	}
	// Wait for the change to run against the fake lmice before the test's
	// environment (DALE_LMICE) is restored; it must never reach a real one.
	waitForJob(t, server, job.ID)
	args, _ := os.ReadFile(argsFile)
	if got := strings.Join(strings.Fields(string(args)), " "); got != "integrations disable gitdcy --device lawnmower --format json" {
		t.Fatalf("change job did not run against the fake lmice: %q", got)
	}
}

func TestLinuxMiceAgentJobsMapToWords(t *testing.T) {
	for kind, want := range map[string]string{
		"agents-start":       "agents start --device lawnmower --yes --format json",
		"agents-stop":        "agents stop --device lawnmower --yes --format json",
		"agents-browser-on":  "agents browser on --device lawnmower --yes --format json",
		"agents-browser-off": "agents browser off --device lawnmower --yes --format json",
		"agents-desktop-on":  "agents desktop on --device lawnmower --yes --format json",
		"agents-desktop-off": "agents desktop off --device lawnmower --yes --format json",
		"projects-tidy":      "projects tidy --yes --format json",
	} {
		args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: kind, Device: "lawnmower"})
		if err != nil || !changes || strings.Join(args, " ") != want {
			t.Fatalf("%s: got %q changes=%v err=%v", kind, args, changes, err)
		}
	}
}

func TestLinuxMiceReleaseJobsMapToWords(t *testing.T) {
	for kind, want := range map[string]string{
		"tool-publish": "tools publish dale --yes --format json",
		"tool-install": "tools install dale --device lawnmower --yes --format json",
	} {
		args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: kind, ID: "dale", Device: "lawnmower"})
		if err != nil || !changes || strings.Join(args, " ") != want {
			t.Fatalf("%s: got %q changes=%v err=%v", kind, args, changes, err)
		}
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "tool-install", ID: "dale", Device: "x;rm"}); err == nil {
		t.Fatal("a bad device name was accepted")
	}
}

func TestLinuxMiceDesktopProfileJobsMapToWords(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "agents-desktop-open", ID: "work", Device: "lawnmower"})
	if err != nil || !changes || strings.Join(args, " ") != "agents desktop open work --device lawnmower --yes --format json" {
		t.Fatalf("got %q changes=%v err=%v", args, changes, err)
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "agents-desktop-close", ID: "../x", Device: "lawnmower"}); err == nil {
		t.Fatal("a bad profile name was accepted")
	}
}

func TestLinuxMiceSetupJobsMapToWords(t *testing.T) {
	for kind, want := range map[string]struct {
		args    string
		changes bool
	}{
		"setup-check": {"setup --device nerv --like lawnmower --format json", false},
		"setup-save":  {"setup save --device nerv --yes --format json", true},
		"setup-apply": {"setup apply --device nerv --like lawnmower --yes --format json", true},
	} {
		args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: kind, Device: "nerv", B: "lawnmower"})
		if err != nil || changes != want.changes || strings.Join(args, " ") != want.args {
			t.Fatalf("%s: got %q changes=%v err=%v", kind, args, changes, err)
		}
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "setup-apply", Device: "nerv", B: "--yes"}); err == nil {
		t.Fatal("a flag was accepted as the --like device")
	}
}

func waitForJob(t *testing.T, server *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, _ := server.linuxmice.job(id)
		if current.State != "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
