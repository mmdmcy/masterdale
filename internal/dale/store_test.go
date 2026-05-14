package dale

import "testing"

func TestStoreAppendSearchDedup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DeviceID = "test-device"
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEvent(cfg.DeviceID, "user", "chat", "message.created", map[string]any{"text": "find me"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.ID = "stable"
	added, err := store.AppendIfNew(e)
	if err != nil || !added {
		t.Fatalf("append new = %v %v", added, err)
	}
	added, err = store.AppendIfNew(e)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("expected duplicate to be skipped")
	}
	results, err := store.Search("find", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "stable" {
		t.Fatalf("unexpected results: %#v", results)
	}
}
