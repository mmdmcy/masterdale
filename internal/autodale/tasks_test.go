package autodale

import (
	"testing"
	"time"
)

func TestAddAndFindDueTask(t *testing.T) {
	sink := DefaultSink()
	sink.DataDir = t.TempDir()
	task, err := AddTask(sink, -time.Minute, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	due, err := DueTasks(sink, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != task.ID {
		t.Fatalf("unexpected due tasks: %#v", due)
	}
	if err := CompleteTask(sink, task.ID); err != nil {
		t.Fatal(err)
	}
	due, err = DueTasks(sink, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("expected completed task to stay quiet: %#v", due)
	}
}
