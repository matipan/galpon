package model

// PlanLaunch records a user-owned foreground implementation. It is not a
// cross-agent delegation or a parent/child ownership relationship.
type PlanLaunch struct {
	SourceAgentID string `json:"sourceAgentId"`
	RevisionID    string `json:"revisionId"`
	PlanHash      string `json:"planHash"`
	AgentID       string `json:"agentId"`
	Created       bool   `json:"created"`
}
