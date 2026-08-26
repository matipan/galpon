package app

import (
	"context"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/store"
)

func TestValidateWorkspaceTitleRejectsHostileText(t *testing.T) {
	if title, err := validateWorkspaceTitle("  Safe workspace  "); err != nil || title != "Safe workspace" {
		t.Fatalf("valid title = %q, %v", title, err)
	}
	values := []string{"", "line\nbreak", "bidi\u202etitle", "separator\u2028title", strings.Repeat("x", 121)}
	for _, value := range values {
		if _, err := validateWorkspaceTitle(value); err == nil {
			t.Fatalf("hostile workspace title was accepted: %q", value)
		}
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	application := &App{Store: st}
	for _, value := range values {
		if _, err := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: value}); err == nil {
			t.Fatalf("workspace creation accepted hostile title: %q", value)
		}
	}
}
