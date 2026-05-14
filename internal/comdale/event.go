package comdale

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Event struct {
	ID        string         `json:"id"`
	Schema    string         `json:"schema"`
	Timestamp string         `json:"timestamp"`
	DeviceID  string         `json:"device_id"`
	Actor     string         `json:"actor"`
	Channel   string         `json:"channel"`
	Kind      string         `json:"kind"`
	Body      map[string]any `json:"body"`
	Refs      []any          `json:"refs"`
	Hash      string         `json:"hash"`
}

type Sink struct {
	DataDir      string
	DeviceID     string
	LearndaleURL string
}

func DefaultSink() Sink {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	dataDir := os.Getenv("COMDALE_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share", "comdale")
	}
	return Sink{DataDir: dataDir, DeviceID: host, LearndaleURL: os.Getenv("LEARDALE_URL")}
}

func NewEvent(deviceID, kind string, body map[string]any) Event {
	return Event{
		ID:        newID(),
		Schema:    "masterdale.event.v1",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		DeviceID:  deviceID,
		Actor:     "comdale",
		Channel:   "commerce",
		Kind:      kind,
		Body:      body,
		Refs:      []any{},
	}
}

func (e *Event) Sign() {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	e.Hash = "sha256:" + hex.EncodeToString(sum[:])
}

func (s Sink) Emit(e Event) error {
	if e.DeviceID == "" {
		e.DeviceID = s.DeviceID
	}
	e.Sign()
	if s.LearndaleURL != "" {
		b, _ := json.Marshal(e)
		resp, err := http.Post(s.LearndaleURL+"/v1/events", "application/json", bytes.NewReader(b))
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode < 300 {
				return nil
			}
		}
	}
	if err := os.MkdirAll(s.DataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.DataDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
