package dale

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcError(nil, -32700, err.Error()))
		return
	}
	result, err := s.dispatchMCP(r, req)
	if err != nil {
		writeJSON(w, http.StatusOK, rpcError(req.ID, -32603, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  result,
	})
}

func (s *Server) dispatchMCP(r *http.Request, req rpcRequest) (any, error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"serverInfo":      map[string]any{"name": "learndale", "version": "0.1.0"},
			"capabilities": map[string]any{
				"resources": map[string]any{"listChanged": true},
				"prompts":   map[string]any{"listChanged": true},
				"tools":     map[string]any{"listChanged": true},
			},
		}, nil
	case "resources/list":
		events, err := s.store.List(100)
		if err != nil {
			return nil, err
		}
		resources := make([]map[string]any, 0, len(events))
		for _, e := range events {
			resources = append(resources, map[string]any{
				"uri":         "event://" + e.ID,
				"name":        e.Kind,
				"title":       e.Channel + "/" + e.Kind,
				"description": Shorten(TextFromBody(e.Body), 120),
				"mimeType":    "application/json",
			})
		}
		return map[string]any{"resources": resources}, nil
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(req.Params, &params)
		events, err := s.store.List(0)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if params.URI == "event://"+e.ID {
				b, _ := json.MarshalIndent(e, "", "  ")
				return map[string]any{"contents": []map[string]any{{
					"uri":      params.URI,
					"mimeType": "application/json",
					"text":     string(b),
				}}}, nil
			}
		}
		return nil, fmt.Errorf("resource not found: %s", params.URI)
	case "prompts/list":
		return map[string]any{"prompts": []map[string]any{
			{"name": "handoff_summary", "title": "Handoff Summary", "description": "Summarize recent work for another agent"},
			{"name": "qa_review", "title": "QA Review", "description": "Review recent outputs and list risks"},
			{"name": "commerce_draft", "title": "Commerce Draft", "description": "Draft approval-gated business content"},
			{"name": "system_diagnosis", "title": "System Diagnosis", "description": "Analyze local health events"},
			{"name": "daily_brief", "title": "Daily Brief", "description": "Summarize tasks, context, and next actions"},
		}}, nil
	case "prompts/get":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		return promptByName(params.Name, params.Arguments), nil
	case "tools/list":
		return map[string]any{"tools": []map[string]any{
			tool("post_message", "Post a message into the local event log"),
			tool("create_task", "Create a local task event"),
			tool("search_resources", "Search local events and summaries"),
			tool("summarize_session", "Return a compact summary for a Codex session event"),
			tool("request_approval", "Record an approval request without executing it"),
		}}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		return s.callTool(params.Name, params.Arguments)
	default:
		return nil, fmt.Errorf("unsupported method: %s", req.Method)
	}
}

func promptByName(name string, args map[string]any) map[string]any {
	subject := cleanArg(args["subject"])
	if subject == "" {
		subject = "the current Masterdale context"
	}
	text := map[string]string{
		"handoff_summary":  "Summarize recent work, blockers, decisions, and next concrete actions for " + subject + ".",
		"qa_review":        "Review the recent outputs for correctness, missing tests, security risks, and unclear assumptions about " + subject + ".",
		"commerce_draft":   "Draft concise approval-gated business content for " + subject + ". Do not publish or imply approval.",
		"system_diagnosis": "Analyze system health events for " + subject + " and return likely causes plus safe next checks.",
		"daily_brief":      "Create a short daily brief with current tasks, active repos, relevant sessions, and next actions for " + subject + ".",
	}[name]
	if text == "" {
		text = "Work with this Masterdale context: " + subject
	}
	return map[string]any{
		"description": name,
		"messages": []map[string]any{{
			"role":    "user",
			"content": map[string]any{"type": "text", "text": text},
		}},
	}
}

func tool(name, description string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": map[string]any{"type": "object"},
	}
}

func (s *Server) callTool(name string, args map[string]any) (any, error) {
	switch name {
	case "post_message":
		text := cleanArg(args["text"])
		e, err := NewEvent(s.cfg.DeviceID, "agent", "chat", "message.created", map[string]any{"text": text}, nil)
		if err == nil {
			err = s.store.Append(e)
		}
		return toolText("posted " + e.ID), err
	case "create_task":
		e, err := NewEvent(s.cfg.DeviceID, "agent", "task", "task.created", args, nil)
		if err == nil {
			err = s.store.Append(e)
		}
		return toolText("created task " + e.ID), err
	case "search_resources":
		results, err := s.store.Search(cleanArg(args["query"]), 10)
		if err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(results, "", "  ")
		return toolText(string(b)), nil
	case "summarize_session":
		results, err := s.store.Search(cleanArg(args["session_id"]), 5)
		if err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(results, "", "  ")
		return toolText(string(b)), nil
	case "request_approval":
		e, err := NewEvent(s.cfg.DeviceID, "agent", "approval", "approval.requested", args, nil)
		if err == nil {
			err = s.store.Append(e)
		}
		return toolText("approval requested " + e.ID), err
	default:
		return nil, fmt.Errorf("unsupported tool: %s", name)
	}
}

func toolText(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func rpcError(id any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
}
