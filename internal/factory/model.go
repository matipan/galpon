package factory

import "slices"

const APIVersion = 5

type Stage string

const (
	StageIntake             Stage = "intake"
	StagePlanning           Stage = "planning"
	StagePlanApproval       Stage = "plan_approval"
	StageImplementation     Stage = "implementation"
	StageHumanTest          Stage = "human_test"
	StageReview             Stage = "review"
	StageReviewFixes        Stage = "review_fixes"
	StagePRCI               Stage = "pr_ci"
	StageWaitingForApproval Stage = "waiting_for_approval"
	StageComplete           Stage = "complete"
	StageFailed             Stage = "failed"
)

var orderedStages = []Stage{StageIntake, StagePlanning, StagePlanApproval, StageImplementation, StageHumanTest, StageReview, StageReviewFixes, StagePRCI, StageWaitingForApproval, StageComplete}

func Stages() []Stage { return slices.Clone(orderedStages) }

type WorkOrder struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Request        string `json:"request"`
	IssueURL       string `json:"issueUrl,omitempty"`
	RepositoryID   string `json:"repositoryId"`
	RepositoryPath string `json:"repositoryPath,omitempty"`
	WorkspaceID    string `json:"workspaceId"`
	BaseRef        string `json:"baseRef,omitempty"`
	Stage          Stage  `json:"stage"`
	Status         string `json:"status"`
	Plan           string `json:"plan,omitempty"`
	DeveloperID    string `json:"developerId,omitempty"`
	Commit         string `json:"commit,omitempty"`
	PRURL          string `json:"prUrl,omitempty"`
	PRNumber       int    `json:"prNumber,omitempty"`
	CIStatus       string `json:"ciStatus,omitempty"`
	LastError      string `json:"lastError,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

type AgentRun struct {
	ID          string `json:"id"`
	WorkOrderID string `json:"workOrderId"`
	AgentID     string `json:"agentId"`
	Kind        string `json:"kind"`
	Commit      string `json:"commit,omitempty"`
	Status      string `json:"status"`
	Result      string `json:"result,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type Event struct {
	ID          string `json:"id"`
	WorkOrderID string `json:"workOrderId"`
	Kind        string `json:"kind"`
	Message     string `json:"message"`
	CreatedAt   int64  `json:"createdAt"`
}

type CreateRequest struct {
	Title          string `json:"title"`
	Request        string `json:"request"`
	IssueURL       string `json:"issueUrl,omitempty"`
	RepositoryID   string `json:"repositoryId"`
	RepositoryPath string `json:"repositoryPath"`
	WorkspaceID    string `json:"workspaceId"`
	BaseRef        string `json:"baseRef,omitempty"`
}

type Snapshot struct {
	WorkOrders []WorkOrder `json:"workOrders"`
	Events     []Event     `json:"events,omitempty"`
	Runs       []AgentRun  `json:"runs,omitempty"`
}

type ActionRequest struct {
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}
