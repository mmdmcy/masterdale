package dale

import "testing"

func TestEventSignVerify(t *testing.T) {
	e, err := NewEvent("device", "user", "chat", "message.created", map[string]any{"text": "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("secret")
	if err := (&e).Sign(secret); err != nil {
		t.Fatal(err)
	}
	if !e.Verify(secret) {
		t.Fatal("expected event to verify")
	}
	e.Body["text"] = "changed"
	if e.Verify(secret) {
		t.Fatal("expected changed event to fail verification")
	}
}
