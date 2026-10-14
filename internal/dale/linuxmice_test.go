package dale

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	view := RunLinuxMiceView(t.Context(), "map", linuxmiceViews["map"])
	if LinuxMiceBinary() == "" && (view.Available || view.Hint == "") {
		t.Fatalf("missing lmice should be reported as unavailable with a hint: %+v", view)
	}
}
