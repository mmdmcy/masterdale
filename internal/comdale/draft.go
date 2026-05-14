package comdale

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/mmdmcy/masterdale/internal/models"
)

type DraftRequest struct {
	Type  string `json:"type"`
	Topic string `json:"topic"`
}

type Draft struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Business  string `json:"business"`
	Type      string `json:"type"`
	Topic     string `json:"topic"`
	Status    string `json:"status"`
	Content   string `json:"content"`
}

func CreateDraft(ctx context.Context, profile BusinessProfile, req DraftRequest) (Draft, error) {
	if req.Type == "" {
		req.Type = "post"
	}
	if req.Topic == "" {
		req.Topic = "local AI automation"
	}
	content := templateDraft(profile, req)
	if os.Getenv("COMDALE_USE_OLLAMA") == "1" {
		if generated, err := ollamaDraft(ctx, profile, req); err == nil && strings.TrimSpace(generated) != "" {
			content = generated
		}
	}
	return Draft{
		ID:        newID(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Business:  profile.Name,
		Type:      req.Type,
		Topic:     req.Topic,
		Status:    "needs_approval",
		Content:   strings.TrimSpace(content),
	}, nil
}

func templateDraft(profile BusinessProfile, req DraftRequest) string {
	const body = `{{.Name}} helps {{index .Audience 0}} with {{index .Offers 0}} and {{index .Offers 1}}.

Topic: {{.Topic}}

Practical angle:
- show the current manual bottleneck
- explain the local/private AI workflow
- give one concrete next step

Draft:
If your work is spread across devices, tools, and half-finished AI sessions, the problem is not intelligence. It is coordination.

{{.Name}} builds privacy-conscious systems that turn scattered work into a clear local workflow: context, tasks, monitoring, and business output stay under your control.

For {{.Topic}}, the first useful step is not a huge platform. It is a small working loop that captures context, checks the system, drafts the next action, and waits for human approval before anything public happens.`
	t := template.Must(template.New("draft").Parse(body))
	var buf bytes.Buffer
	_ = t.Execute(&buf, map[string]any{
		"Name":     profile.Name,
		"Audience": safeList(profile.Audience),
		"Offers":   safeList(profile.Offers),
		"Topic":    req.Topic,
	})
	return buf.String()
}

func ollamaDraft(ctx context.Context, profile BusinessProfile, req DraftRequest) (string, error) {
	url := os.Getenv("OLLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	model := os.Getenv("COMDALE_MODEL")
	if model == "" {
		model = os.Getenv("DALE_MODEL")
	}
	if model == "" {
		model = os.Getenv("DALE_PRIMARY_MODEL")
	}
	if model == "" {
		model = models.PrimaryDefault
	}
	prompt := "Draft concise approval-gated business content. Do not claim it was published. Business: " + profile.Name + ". Voice: " + profile.Voice + ". Topic: " + req.Topic + ". Type: " + req.Type
	payload := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]any{{
			"role":    "user",
			"content": prompt,
		}},
		"options": map[string]any{"temperature": 0.2},
	}
	b, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var decoded struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", err
	}
	return decoded.Message.Content, nil
}

func safeList(items []string) []string {
	if len(items) >= 2 {
		return items
	}
	out := append([]string{}, items...)
	for len(out) < 2 {
		out = append(out, "customers")
	}
	return out
}
