package dale

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const EventSchema = "masterdale.event.v1"

type Ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	URI  string `json:"uri,omitempty"`
}

type Event struct {
	ID        string         `json:"id"`
	Schema    string         `json:"schema"`
	Timestamp string         `json:"timestamp"`
	DeviceID  string         `json:"device_id"`
	Actor     string         `json:"actor"`
	Channel   string         `json:"channel"`
	Kind      string         `json:"kind"`
	Body      map[string]any `json:"body"`
	Refs      []Ref          `json:"refs"`
	Hash      string         `json:"hash"`
}

func NewEvent(deviceID, actor, channel, kind string, body map[string]any, refs []Ref) (Event, error) {
	if deviceID == "" {
		return Event{}, errors.New("device id is required")
	}
	if actor == "" || channel == "" || kind == "" {
		return Event{}, errors.New("actor, channel, and kind are required")
	}
	if body == nil {
		body = map[string]any{}
	}
	return Event{
		ID:        NewID(),
		Schema:    EventSchema,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		DeviceID:  deviceID,
		Actor:     actor,
		Channel:   channel,
		Kind:      kind,
		Body:      body,
		Refs:      refs,
	}, nil
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func DeterministicID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

func (e Event) canonicalBytes() ([]byte, error) {
	e.Hash = ""
	if e.Schema == "" {
		e.Schema = EventSchema
	}
	if e.Body == nil {
		e.Body = map[string]any{}
	}
	if e.Refs == nil {
		e.Refs = []Ref{}
	}
	return json.Marshal(e)
}

func (e *Event) Sign(secret []byte) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Schema == "" {
		e.Schema = EventSchema
	}
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := e.canonicalBytes()
	if err != nil {
		return err
	}
	if len(secret) == 0 {
		sum := sha256.Sum256(b)
		e.Hash = "sha256:" + hex.EncodeToString(sum[:])
		return nil
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(b)
	e.Hash = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	return nil
}

func (e Event) Verify(secret []byte) bool {
	if e.Hash == "" {
		return false
	}
	want := e.Hash
	if err := (&e).Sign(secret); err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(e.Hash))
}

func TextFromBody(body map[string]any) string {
	if body == nil {
		return ""
	}
	for _, key := range []string{"text", "summary", "title", "prompt", "response"} {
		if v, ok := body[key].(string); ok {
			return v
		}
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func Shorten(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
