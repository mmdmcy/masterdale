package autodale

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Task struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	DueAt     string `json:"due_at"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
}

func AddTask(sink Sink, after time.Duration, text string) (Task, error) {
	if strings.TrimSpace(text) == "" {
		return Task{}, errors.New("task text is required")
	}
	task := Task{
		ID:        newID(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		DueAt:     time.Now().Add(after).UTC().Format(time.RFC3339Nano),
		Text:      text,
	}
	if err := appendTask(sink, task); err != nil {
		return Task{}, err
	}
	return task, sink.Emit(NewEvent(sink.DeviceID, "task.scheduled", map[string]any{
		"task_id": task.ID,
		"due_at":  task.DueAt,
		"text":    task.Text,
	}))
}

func DueTasks(sink Sink, now time.Time) ([]Task, error) {
	tasks, err := readTasks(sink)
	if err != nil {
		return nil, err
	}
	var due []Task
	for _, task := range tasks {
		if task.Done {
			continue
		}
		dueAt, err := time.Parse(time.RFC3339Nano, task.DueAt)
		if err == nil && !dueAt.After(now) {
			due = append(due, task)
		}
	}
	return due, nil
}

func CompleteTask(sink Sink, id string) error {
	tasks, err := readTasks(sink)
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			tasks[i].Done = true
		}
	}
	return writeTasks(sink, tasks)
}

func appendTask(sink Sink, task Task) error {
	if err := os.MkdirAll(sink.DataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(sink.DataDir, "tasks.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(task)
	_, err = f.Write(append(b, '\n'))
	return err
}

func readTasks(sink Sink) ([]Task, error) {
	f, err := os.Open(filepath.Join(sink.DataDir, "tasks.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Task
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var task Task
		if err := json.Unmarshal(scanner.Bytes(), &task); err == nil {
			out = append(out, task)
		}
	}
	return out, scanner.Err()
}

func writeTasks(sink Sink, tasks []Task) error {
	if err := os.MkdirAll(sink.DataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(sink.DataDir, "tasks.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, task := range tasks {
		b, _ := json.Marshal(task)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}
