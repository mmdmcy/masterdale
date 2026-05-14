package comdale

import (
	"context"
	"testing"
)

func TestCreateDraftNeedsApproval(t *testing.T) {
	profile := BusinessProfile{
		Name:        "Demo",
		Audience:    []string{"small teams"},
		Offers:      []string{"AI automation", "SaaS development"},
		Voice:       "direct",
		RepoPaths:   nil,
		Description: "demo",
	}
	draft, err := CreateDraft(context.Background(), profile, DraftRequest{Topic: "local agents"})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Status != "needs_approval" {
		t.Fatalf("draft must require approval: %#v", draft)
	}
	if draft.Content == "" {
		t.Fatal("expected content")
	}
}
