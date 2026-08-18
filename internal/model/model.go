package model

type Repository struct {
	ID            string             `json:"id"`
	Title         string             `json:"title"`
	SourcePath    string             `json:"sourcePath"`
	FetchURL      string             `json:"fetchUrl"`
	MirrorPath    string             `json:"mirrorPath"`
	DefaultRemote string             `json:"defaultRemote"`
	PushRemote    string             `json:"pushRemote,omitempty"`
	Remotes       []RepositoryRemote `json:"remotes"`
	DefaultBranch string             `json:"defaultBranch"`
	CreatedAt     int64              `json:"createdAt"`
}

type RepositoryRemote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetchUrl"`
	PushURL  string `json:"pushUrl"`
}

type Workspace struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Renderer        string `json:"renderer,omitempty"`
	RendererContext string `json:"rendererContext,omitempty"`
	RendererID      string `json:"rendererId,omitempty"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
}

type Worktree struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspaceId"`
	RepositoryID string `json:"repositoryId"`
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	BaseRef      string `json:"baseRef"`
	SourceRemote string `json:"sourceRemote,omitempty"`
	Lifecycle    string `json:"lifecycle"`
	CreatedAt    int64  `json:"createdAt"`
}

type AgentWorktree struct {
	WorktreeID string `json:"worktreeId"`
	Position   int    `json:"position"`
	Mode       string `json:"mode"`
}

type AgentPlacement struct {
	Type              string          `json:"type"`
	CWD               string          `json:"cwd,omitempty"`
	PrimaryWorktreeID string          `json:"primaryWorktreeId,omitempty"`
	Worktrees         []AgentWorktree `json:"worktrees,omitempty"`
}

type Agent struct {
	ID               string         `json:"id"`
	WorkspaceID      string         `json:"workspaceId"`
	Title            string         `json:"title"`
	Role             string         `json:"role,omitempty"`
	CreatedByAgentID string         `json:"createdByAgentId,omitempty"`
	Presentation     string         `json:"presentation"`
	ContextAgentID   string         `json:"contextAgentId,omitempty"`
	Placement        AgentPlacement `json:"placement"`
	Kind             string         `json:"kind"`
	Status           string         `json:"status"`
	SessionID        string         `json:"sessionId"`
	SessionPath      string         `json:"sessionPath,omitempty"`
	Renderer         string         `json:"renderer,omitempty"`
	RendererContext  string         `json:"rendererContext,omitempty"`
	RendererID       string         `json:"rendererId,omitempty"`
	RuntimeID        string         `json:"runtimeId,omitempty"`
	LastError        string         `json:"lastError,omitempty"`
	CreatedAt        int64          `json:"createdAt"`
	UpdatedAt        int64          `json:"updatedAt"`
}

type AgentMessage struct {
	ID             string `json:"id"`
	SenderAgentID  string `json:"senderAgentId,omitempty"`
	TargetAgentID  string `json:"targetAgentId"`
	Kind           string `json:"kind"`
	ReplyTo        string `json:"replyTo,omitempty"`
	Prompt         string `json:"prompt"`
	Status         string `json:"status"`
	Response       string `json:"response,omitempty"`
	Error          string `json:"error,omitempty"`
	LastError      string `json:"lastError,omitempty"`
	RuntimeID      string `json:"runtimeId,omitempty"`
	IdempotencyKey string `json:"-"`
	ClaimKey       string `json:"-"`
	Attempt        int    `json:"attempt"`
	ClaimedAt      int64  `json:"claimedAt,omitempty"`
	LeaseExpiresAt int64  `json:"leaseExpiresAt,omitempty"`
	CompletedAt    int64  `json:"completedAt,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

// ConversationEvent is a normalized Pi event. It intentionally does not store
// Pi thinking blocks or the raw session entry.
type ConversationEvent struct {
	Sequence   int64  `json:"seq"`
	AgentID    string `json:"agentId,omitempty"`
	EventID    string `json:"eventId"`
	RuntimeSeq int64  `json:"runtimeSeq,omitempty"`
	Kind       string `json:"kind"`
	PiEntryID  string `json:"piEntryId,omitempty"`
	Role       string `json:"role,omitempty"`
	Content    string `json:"content,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	IsDelta    bool   `json:"isDelta,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

type CompanionEvent struct {
	Sequence    int64  `json:"sequence"`
	Type        string `json:"type"`
	AgentID     string `json:"agentId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

type Dashboard struct {
	Repositories []Repository `json:"repositories"`
	Workspaces   []Workspace  `json:"workspaces"`
	Worktrees    []Worktree   `json:"worktrees"`
	Agents       []Agent      `json:"agents"`
}

// DurableState is the logical state that a checkpoint can move to another
// Galpon installation. It does not include soft-deleted resources.
type DurableState struct {
	Repositories []Repository   `json:"repositories"`
	Workspaces   []Workspace    `json:"workspaces"`
	Worktrees    []Worktree     `json:"worktrees"`
	Agents       []Agent        `json:"agents"`
	Messages     []AgentMessage `json:"messages"`
}

type AgentView struct {
	Agent     Agent          `json:"agent"`
	Worktrees []Worktree     `json:"worktrees,omitempty"`
	Messages  []AgentMessage `json:"messages"`
}

type ResourceCounts struct {
	Repositories int `json:"repositories"`
	Workspaces   int `json:"workspaces"`
	Worktrees    int `json:"worktrees"`
	Agents       int `json:"agents"`
}

type DeletionResult struct {
	Kind   string         `json:"kind"`
	ID     string         `json:"id"`
	Hidden ResourceCounts `json:"hidden"`
}

type CleanupResult struct {
	Removed ResourceCounts `json:"removed"`
}

type AgentCleanupResult struct {
	Removed     ResourceCounts    `json:"removed"`
	Agents      []CleanedAgentRef `json:"agents"`
	ClosedViews int               `json:"closedViews"`
}

type CleanedAgentRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (a Agent) IsBackground() bool { return a.Presentation == "background" }

func (d Dashboard) Worktree(id string) (Worktree, bool) {
	for _, item := range d.Worktrees {
		if item.ID == id {
			return item, true
		}
	}
	return Worktree{}, false
}

func (d Dashboard) Workspace(id string) (Workspace, bool) {
	for _, item := range d.Workspaces {
		if item.ID == id {
			return item, true
		}
	}
	return Workspace{}, false
}

func (d Dashboard) Agent(id string) (Agent, bool) {
	for _, item := range d.Agents {
		if item.ID == id {
			return item, true
		}
	}
	return Agent{}, false
}

func (d Dashboard) Repository(id string) (Repository, bool) {
	for _, item := range d.Repositories {
		if item.ID == id {
			return item, true
		}
	}
	return Repository{}, false
}

func (d Dashboard) AgentWorktrees(agent Agent) []Worktree {
	out := make([]Worktree, 0, len(agent.Placement.Worktrees))
	for _, assignment := range agent.Placement.Worktrees {
		if worktree, ok := d.Worktree(assignment.WorktreeID); ok {
			out = append(out, worktree)
		}
	}
	return out
}

func (d Dashboard) PrimaryWorktree(agent Agent) (Worktree, bool) {
	if agent.Placement.PrimaryWorktreeID == "" {
		return Worktree{}, false
	}
	return d.Worktree(agent.Placement.PrimaryWorktreeID)
}
