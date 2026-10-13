package sessionfile

import (
	"strings"
	"testing"
)

func TestClaudeIdentityFollowsExtensibleMetadata(t *testing.T) {
	const id = "825cdb31-a88b-43f8-a452-61a51f08baaa"
	input := `{"type":"future-native-metadata","sessionId":"not-a-session"}` + "\n" + `{"type":"user","sessionId":"` + id + `","message":{"role":"user","content":"Work"}}` + "\n"
	got, err := NativeID("claude", strings.NewReader(input))
	if err != nil || got != id {
		t.Fatalf("native session identity = %q, %v", got, err)
	}
	invalid := strings.Replace(input, id, "../../outside-session", 1)
	if _, err := NativeID("claude", strings.NewReader(invalid)); err == nil {
		t.Fatal("unsafe native session identity was accepted")
	}
	if _, err := NativeID("claude", strings.NewReader(`{"type":"future-native-metadata","sessionId":"`+id+`"}`)); err == nil {
		t.Fatal("metadata alone established an unsupported session format")
	}
}
