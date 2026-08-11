package terminal

import (
	"context"

	"github.com/matipan/galpon/internal/model"
)

// Renderer turns durable Galpon state into terminal views. Its external IDs
// are disposable references and never identify a workspace or agent.
type Renderer interface {
	Name() string
	Context() string
	OpenTerminal(context.Context, model.Workspace, model.Worktree, string, []string) (string, error)
	OpenAgent(context.Context, model.Workspace, model.Worktree, model.Agent, []string, bool) (string, string, bool, error)
	CloseAgent(context.Context, model.Agent) error
	ReportAgent(context.Context, model.Agent, string, string) error
}
