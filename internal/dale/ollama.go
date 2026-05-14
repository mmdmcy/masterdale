package dale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type CompleteRequest struct {
	Prompt         string         `json:"prompt"`
	Model          string         `json:"model,omitempty"`
	Role           string         `json:"role,omitempty"`
	Schema         map[string]any `json:"schema,omitempty"`
	Images         []string       `json:"images,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Think          *bool          `json:"think,omitempty"`
	KeepAlive      string         `json:"keep_alive,omitempty"`
	PlainText      bool           `json:"plain_text,omitempty"`
}

type CompleteResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Valid    bool   `json:"valid"`
	Error    string `json:"error,omitempty"`
}

func CompleteWithOllama(ctx context.Context, cfg Config, req CompleteRequest) (CompleteResponse, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return CompleteResponse{}, errors.New("prompt is required")
	}
	model := req.Model
	if model == "" {
		if len(req.Images) > 0 {
			model = cfg.Models.ForRole("vision")
		} else {
			model = cfg.Models.ForRole(req.Role)
		}
	}
	options := map[string]any{"temperature": 0}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	payload := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]any{{
			"role":    "user",
			"content": req.Prompt,
		}},
		"options": options,
	}
	if req.Think != nil {
		payload["think"] = *req.Think
	}
	if req.KeepAlive != "" {
		payload["keep_alive"] = req.KeepAlive
	}
	if len(req.Images) > 0 {
		payload["messages"].([]map[string]any)[0]["images"] = req.Images
	}
	if req.Schema != nil {
		payload["format"] = req.Schema
	} else if !req.PlainText {
		payload["format"] = "json"
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return CompleteResponse{}, err
	}
	timeout := 90 * time.Second
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := strings.TrimRight(cfg.OllamaURL, "/") + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return CompleteResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return CompleteResponse{Model: model, Valid: false, Error: err.Error()}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if resp.StatusCode >= 300 {
		return CompleteResponse{Model: model, Valid: false, Error: string(body)}, fmt.Errorf("ollama returned %s", resp.Status)
	}
	var decoded struct {
		Model   string `json:"model"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return CompleteResponse{Model: model, Valid: false, Error: err.Error()}, err
	}
	out := CompleteResponse{Model: model, Response: decoded.Message.Content, Valid: true}
	if !req.PlainText && (req.Schema != nil || payload["format"] == "json") {
		var js any
		if err := json.Unmarshal([]byte(decoded.Message.Content), &js); err != nil {
			out.Valid = false
			out.Error = "response was not valid JSON: " + err.Error()
		}
	}
	return out, nil
}
