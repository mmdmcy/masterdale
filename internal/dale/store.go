package dale

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Store struct {
	path   string
	secret []byte
	mu     sync.Mutex
}

func OpenStore(cfg Config) (*Store, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	return &Store{
		path:   filepath.Join(cfg.DataDir, "events.jsonl"),
		secret: cfg.Secret(),
	}, nil
}

func (s *Store) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Hash == "" || !e.Verify(s.secret) {
		if err := (&e).Sign(s.secret); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *Store) AppendIfNew(e Event) (bool, error) {
	if e.ID == "" {
		return false, errors.New("event id required for AppendIfNew")
	}
	exists, err := s.Has(e.ID)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	return true, s.Append(e)
}

func (s *Store) Has(id string) (bool, error) {
	events, err := s.List(0)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) List(limit int) ([]Event, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp < events[j].Timestamp
	})
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

type SearchResult struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Channel   string `json:"channel"`
	Kind      string `json:"kind"`
	Snippet   string `json:"snippet"`
}

func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	events, err := s.List(0)
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		haystack := strings.ToLower(e.Channel + " " + e.Kind + " " + TextFromBody(e.Body))
		if query == "" || strings.Contains(haystack, query) {
			out = append(out, SearchResult{
				ID:        e.ID,
				Timestamp: e.Timestamp,
				Channel:   e.Channel,
				Kind:      e.Kind,
				Snippet:   Shorten(TextFromBody(e.Body), 240),
			})
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
