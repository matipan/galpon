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

type AgentWaitError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// AgentWaitResult keeps the message fields at the top level for compatibility
// and adds explicit wait state and target runtime state.
type AgentWaitResult struct {
	AgentMessage
	MessageID           string          `json:"messageId"`
	WaitStatus          string          `json:"waitStatus"`
	MessageStatus       string          `json:"messageStatus"`
	TargetRuntimeStatus string          `json:"targetRuntimeStatus"`
	WaitError           *AgentWaitError `json:"waitError,omitempty"`
}

type AgentWaitManyResult struct {
	Status     string            `json:"status"`
	ReturnWhen string            `json:"returnWhen"`
	Completed  int               `json:"completed"`
	Total      int               `json:"total"`
	Outcomes   []AgentWaitResult `json:"outcomes"`
}

// ImageAttachment is an image blob that belongs to a message or conversation
// event. Data is base64. Public Companion responses do not include it.
type ImageAttachment struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	URL      string `json:"url,omitempty"`
	Data     string `json:"data,omitempty"`
}

type AgentMessage struct {
	ID                   string             `json:"id"`
	SenderAgentID        string             `json:"senderAgentId,omitempty"`
	SenderTitle          string             `json:"senderTitle,omitempty"`
	TargetAgentID        string             `json:"targetAgentId"`
	Kind                 string             `json:"kind"`
	Act                  string             `json:"act"`
	ResultMode           string             `json:"resultMode"`
	ReplyTo              string             `json:"replyTo,omitempty"`
	ParentMessageID      string             `json:"parentMessageId,omitempty"`
	RootMessageID        string             `json:"rootMessageId,omitempty"`
	RunID                string             `json:"runId,omitempty"`
	Depth                int                `json:"depth"`
	Prompt               string             `json:"prompt"`
	Images               *[]ImageAttachment `json:"images,omitempty"`
	Status               string             `json:"status"`
	NotificationState    string             `json:"notificationState,omitempty"`
	Response             string             `json:"response,omitempty"`
	Error                string             `json:"error,omitempty"`
	LastError            string             `json:"lastError,omitempty"`
	TerminalReason       string             `json:"terminalReason,omitempty"`
	RuntimeID            string             `json:"runtimeId,omitempty"`
	IdempotencyKey       string             `json:"-"`
	ClaimKey             string             `json:"-"`
	Attempt              int                `json:"attempt"`
	ClaimedAt            int64              `json:"claimedAt,omitempty"`
	LeaseExpiresAt       int64              `json:"leaseExpiresAt,omitempty"`
	QueueDeadlineAt      int64              `json:"queueDeadlineAt,omitempty"`
	ProcessingDeadlineAt int64              `json:"processingDeadlineAt,omitempty"`
	CompletedAt          int64              `json:"completedAt,omitempty"`
	CreatedAt            int64              `json:"createdAt"`
	UpdatedAt            int64              `json:"updatedAt"`
}

type LifecycleEvent struct {
	ID               string `json:"id"`
	EventType        string `json:"eventType"`
	SubjectAgentID   string `json:"subjectAgentId,omitempty"`
	RecipientAgentID string `json:"recipientAgentId"`
	MessageID        string `json:"messageId,omitempty"`
	Payload          string `json:"payload"`
	CoalesceKey      string `json:"coalesceKey,omitempty"`
	Status           string `json:"status"`
	CreatedAt        int64  `json:"createdAt"`
	DeliveredAt      int64  `json:"deliveredAt,omitempty"`
}

type WorkMilestone struct {
	Label string `json:"label"`
	State string `json:"state"`
}

type WorkCount struct {
	Label     string `json:"label"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
}

// WorkProgressEvent is an agent-reported safe checkpoint. RuntimeID and Attempt
// fence writes but are not included in public work projections.
type WorkProgressEvent struct {
	Sequence   int64           `json:"sequence,omitempty"`
	MessageID  string          `json:"messageId"`
	EventID    string          `json:"eventId"`
	RuntimeID  string          `json:"-"`
	Attempt    int             `json:"attempt"`
	Version    int             `json:"version"`
	Phase      string          `json:"phase"`
	Summary    string          `json:"summary"`
	Milestones []WorkMilestone `json:"milestones,omitempty"`
	Blocker    string          `json:"blocker,omitempty"`
	Counts     []WorkCount     `json:"counts,omitempty"`
	CreatedAt  int64           `json:"createdAt"`
}

type WorkObservation struct {
	State           string `json:"state"`
	Source          string `json:"source"`
	ObservedAt      int64  `json:"observedAt"`
	Lease           string `json:"lease"`
	LeaseObservedAt int64  `json:"leaseObservedAt,omitempty"`
	Attempt         int    `json:"attempt"`
	ResultMode      string `json:"resultMode"`
	Act             string `json:"act"`
	FreshnessAt     int64  `json:"freshnessAt,omitempty"`
}

type WorkActivity struct {
	Category   string `json:"category"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	ObservedAt int64  `json:"observedAt"`
}

type WorkCheckpoint struct {
	Phase      string          `json:"phase"`
	Summary    string          `json:"summary"`
	Milestones []WorkMilestone `json:"milestones,omitempty"`
	Blocker    string          `json:"blocker,omitempty"`
	Counts     []WorkCount     `json:"counts,omitempty"`
	Source     string          `json:"source"`
	ReportedAt int64           `json:"reportedAt"`
}

type WorkTimelineEvent struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"createdAt"`
}

type WorkProjection struct {
	Items         []WorkItem `json:"work"`
	ReturnedRoots int        `json:"returnedRoots"`
	ReturnedItems int        `json:"returnedItems"`
	Truncated     bool       `json:"truncated"`
}

// WorkspaceOperations is the bounded read-only operations projection for one
// workspace. Observations are daemon facts. Checkpoints are agent reports.
type WorkspaceOperations struct {
	Version    int                      `json:"version"`
	Workspace  OperationsWorkspace      `json:"workspace"`
	Summary    OperationsSummary        `json:"summary"`
	Queue      OperationsQueue          `json:"queue"`
	Agents     []OperationsAgent        `json:"agents"`
	Work       []WorkItem               `json:"work"`
	Activity   *OperationsActivityLane  `json:"activity,omitempty"`
	Timeline   []OperationsTimelineFact `json:"timeline"`
	Truncation OperationsTruncation     `json:"truncation"`
}

type OperationsWorkspace struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type OperationsSummary struct {
	Agents            int  `json:"agents"`
	ActiveAgents      int  `json:"activeAgents"`
	ActiveWork        int  `json:"activeWork"`
	QueuedWork        int  `json:"queuedWork"`
	ReportedBlockers  int  `json:"reportedBlockers"`
	StaleObservations int  `json:"staleObservations"`
	RecentFailures    int  `json:"recentFailures"`
	RecentCompletions int  `json:"recentCompletions"`
	WorkCountsExact   bool `json:"workCountsExact"`
}

type OperationsQueue struct {
	InboundQueued       int `json:"inboundQueued"`
	InboundClaimed      int `json:"inboundClaimed"`
	InboundClaimedFresh int `json:"inboundClaimedFresh"`
	ResultsReady        int `json:"resultsReady"`
	ResultDeliveries    int `json:"resultDeliveries"`
	ResultClaims        int `json:"resultClaims"`
}

type OperationsActivityLane struct {
	Version    int                      `json:"version"`
	Facts      []OperationsActivityFact `json:"facts"`
	Truncation OperationsLaneTruncation `json:"truncation"`
}

type OperationsActivityFact struct {
	Category   string `json:"category"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	ObservedAt int64  `json:"observedAt"`
}

type OperationsLaneTruncation struct {
	Truncated     bool `json:"truncated"`
	MaxFacts      int  `json:"maxFacts"`
	FactsOmitted  int  `json:"factsOmitted"`
	OmissionExact bool `json:"omissionExact"`
}

type OperationsAgent struct {
	ID               string              `json:"id"`
	Title            string              `json:"title"`
	Role             string              `json:"role,omitempty"`
	Status           string              `json:"status"`
	Presentation     string              `json:"presentation"`
	UpdatedAt        int64               `json:"updatedAt"`
	CurrentDelivery  *OperationsDelivery `json:"currentDelivery,omitempty"`
	ObservedDelivery *OperationsDelivery `json:"observedDelivery,omitempty"`
}

type OperationsDelivery struct {
	WorkID      string          `json:"workId"`
	Title       string          `json:"title"`
	Observation WorkObservation `json:"observation"`
	Activity    *WorkActivity   `json:"activity,omitempty"`
	Checkpoint  *WorkCheckpoint `json:"checkpoint,omitempty"`
	UpdatedAt   int64           `json:"updatedAt"`
}

type OperationsTimelineFact struct {
	WorkID      string `json:"workId"`
	WorkTitle   string `json:"workTitle"`
	TargetTitle string `json:"targetTitle"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Source      string `json:"source"`
	CreatedAt   int64  `json:"createdAt"`
}

type OperationsTruncation struct {
	Truncated             bool `json:"truncated"`
	MaxAgents             int  `json:"maxAgents"`
	MaxRoots              int  `json:"maxRoots"`
	MaxItems              int  `json:"maxItems"`
	MaxTimeline           int  `json:"maxTimeline"`
	AgentsOmitted         int  `json:"agentsOmitted"`
	RootsOmitted          int  `json:"rootsOmitted"`
	ItemsOmitted          int  `json:"itemsOmitted"`
	TimelineOmitted       int  `json:"timelineOmitted"`
	AgentsOmissionExact   bool `json:"agentsOmissionExact"`
	RootsOmissionExact    bool `json:"rootsOmissionExact"`
	ItemsOmissionExact    bool `json:"itemsOmissionExact"`
	TimelineOmissionExact bool `json:"timelineOmissionExact"`
	SourceTruncated       bool `json:"sourceTruncated"`
}

type WorkItem struct {
	ID             string              `json:"id"`
	Title          string              `json:"title"`
	TargetAgentID  string              `json:"targetAgentId,omitempty"`
	TargetTitle    string              `json:"targetTitle"`
	DelegatorTitle string              `json:"delegatorTitle,omitempty"`
	Priority       string              `json:"priority,omitempty"`
	Depth          int                 `json:"depth"`
	CreatedAt      int64               `json:"createdAt"`
	UpdatedAt      int64               `json:"updatedAt"`
	CompletedAt    int64               `json:"completedAt,omitempty"`
	Observation    WorkObservation     `json:"observation"`
	Activity       *WorkActivity       `json:"activity,omitempty"`
	Checkpoint     *WorkCheckpoint     `json:"checkpoint,omitempty"`
	Result         *OperationsResult   `json:"result,omitempty"`
	Timeline       []WorkTimelineEvent `json:"timeline,omitempty"`
	Children       []WorkItem          `json:"children,omitempty"`
}

type OperationsResult struct {
	Stage      string `json:"stage"`
	Label      string `json:"label"`
	Source     string `json:"source"`
	ObservedAt int64  `json:"observedAt"`
	Lease      string `json:"lease,omitempty"`
}

func (w WorkItem) Active() bool {
	return w.Observation.State == "queued" || w.Observation.State == "started"
}

// ConversationEvent is a normalized Pi event. It stores bounded discussion text
// for the single-user companion, but it does not store the raw Pi session entry.
type ConversationEvent struct {
	Sequence            int64             `json:"seq"`
	AgentID             string            `json:"agentId,omitempty"`
	EventID             string            `json:"eventId"`
	ClientRequestID     string            `json:"clientRequestId,omitempty"`
	RuntimeSeq          int64             `json:"runtimeSeq,omitempty"`
	Kind                string            `json:"kind"`
	PiEntryID           string            `json:"piEntryId,omitempty"`
	Role                string            `json:"role,omitempty"`
	Content             string            `json:"content,omitempty"`
	Images              []ImageAttachment `json:"images,omitempty"`
	ToolName            string            `json:"toolName,omitempty"`
	ToolCallID          string            `json:"toolCallId,omitempty"`
	IsDelta             bool              `json:"isDelta,omitempty"`
	IsError             bool              `json:"isError,omitempty"`
	IsAnchor            bool              `json:"isAnchor,omitempty"`
	IsAgentDelivery     bool              `json:"isAgentDelivery,omitempty"`
	DeliveryKind        string            `json:"deliveryKind,omitempty"`
	DeliverySenderTitle string            `json:"deliverySenderTitle,omitempty"`
	CreatedAt           int64             `json:"createdAt"`
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
	Repositories            []Repository        `json:"repositories"`
	Workspaces              []Workspace         `json:"workspaces"`
	Worktrees               []Worktree          `json:"worktrees"`
	Agents                  []Agent             `json:"agents"`
	Messages                []AgentMessage      `json:"messages"`
	MessageIdempotencyKeys  map[string]string   `json:"messageIdempotencyKeys,omitempty"`
	LifecycleEvents         []LifecycleEvent    `json:"lifecycleEvents,omitempty"`
	WorkProgressEvents      []WorkProgressEvent `json:"workProgressEvents,omitempty"`
	WorkProgressRestoreKeys []string            `json:"workProgressRestoreKeys,omitempty"`
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
