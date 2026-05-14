package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmdmcy/masterdale/internal/comdale"
	"github.com/mmdmcy/masterdale/internal/envfile"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "comdale:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if _, _, err := envfile.LoadUp(".env"); err != nil {
		return err
	}
	if len(args) == 0 {
		usage()
		return nil
	}
	profilePath := stringFlag(args, "--profile")
	args = stripFlag(args, "--profile")
	profile, err := comdale.LoadProfile(profilePath)
	if err != nil {
		return err
	}
	sink := comdale.DefaultSink()
	switch args[0] {
	case "brief":
		err := sink.Emit(comdale.NewEvent(sink.DeviceID, "business.brief", map[string]any{"profile": profile}))
		return printJSON(profile, err)
	case "draft":
		req := parseDraft(args[1:])
		draft, err := comdale.CreateDraft(context.Background(), profile, req)
		if err == nil {
			err = sink.Emit(comdale.NewEvent(sink.DeviceID, "draft.created", map[string]any{"draft": draft}))
		}
		return printJSON(draft, err)
	case "campaign":
		topic := strings.TrimSpace(strings.Join(args[1:], " "))
		if topic == "" {
			topic = "Masterdale demo"
		}
		plan := map[string]any{
			"topic":  topic,
			"status": "needs_approval",
			"steps": []string{
				"write a concise positioning post",
				"draft one customer-facing example",
				"wait 24 hours",
				"review engagement and draft a follow-up",
			},
		}
		err := sink.Emit(comdale.NewEvent(sink.DeviceID, "campaign.planned", plan))
		return printJSON(plan, err)
	case "repos":
		if len(args) >= 2 && args[1] == "scan" {
			base, _ := filepath.Abs(".")
			summaries := comdale.ScanRepos(profile, base)
			err := sink.Emit(comdale.NewEvent(sink.DeviceID, "repos.scanned", map[string]any{"repos": summaries}))
			return printJSON(summaries, err)
		}
	}
	usage()
	return nil
}

func parseDraft(args []string) comdale.DraftRequest {
	req := comdale.DraftRequest{Type: "post"}
	var topic []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--type":
			if i+1 < len(args) {
				req.Type = args[i+1]
				i++
			}
		case "--topic":
			if i+1 < len(args) {
				topic = append(topic, args[i+1])
				i++
			}
		default:
			topic = append(topic, args[i])
		}
	}
	req.Topic = strings.Join(topic, " ")
	return req
}

func stringFlag(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func stripFlag(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func printJSON(v any, err error) error {
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func usage() {
	fmt.Println(`comdale commands:
  --profile path brief
  --profile path draft [--type post] [--topic topic]
  --profile path campaign <topic>
  --profile path repos scan`)
}
