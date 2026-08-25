package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

// DurableState returns all resources that are not marked for cleanup. Archived
// workspaces stay in the export even though the command center hides them.
func (s *Store) DurableState(ctx context.Context) (model.DurableState, error) {
	out := model.DurableState{
		Repositories: []model.Repository{}, Workspaces: []model.Workspace{},
		Worktrees: []model.Worktree{}, Agents: []model.Agent{}, Messages: []model.AgentMessage{},
		MessageIdempotencyKeys: map[string]string{}, LifecycleEvents: []model.LifecycleEvent{}, WorkProgressEvents: []model.WorkProgressEvent{},
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `select id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at
from repositories where not exists (select 1 from deleted_items where kind='repository' and resource_id=repositories.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value model.Repository
		if err := rows.Scan(&value.ID, &value.Title, &value.SourcePath, &value.FetchURL, &value.MirrorPath, &value.DefaultRemote, &value.PushRemote, &value.DefaultBranch, &value.CreatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Repositories = append(out.Repositories, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	repositoryIndex := make(map[string]int, len(out.Repositories))
	for index := range out.Repositories {
		repositoryIndex[out.Repositories[index].ID] = index
	}
	remoteRows, err := tx.QueryContext(ctx, `select repository_id,name,fetch_url,push_url from repository_remotes order by repository_id,name`)
	if err != nil {
		return out, err
	}
	for remoteRows.Next() {
		var repositoryID string
		var remote model.RepositoryRemote
		if err := remoteRows.Scan(&repositoryID, &remote.Name, &remote.FetchURL, &remote.PushURL); err != nil {
			_ = remoteRows.Close()
			return out, err
		}
		if index, ok := repositoryIndex[repositoryID]; ok {
			out.Repositories[index].Remotes = append(out.Repositories[index].Remotes, remote)
		}
	}
	if err := remoteRows.Close(); err != nil {
		return out, err
	}

	rows, err = tx.QueryContext(ctx, `select id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at
from workstreams where not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value model.Workspace
		if err := rows.Scan(&value.ID, &value.Title, &value.Status, &value.Renderer, &value.RendererContext, &value.RendererID, &value.CreatedAt, &value.UpdatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Workspaces = append(out.Workspaces, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = tx.QueryContext(ctx, `select id,workstream_id,repository_id,path,branch,base_ref,source_remote,lifecycle,created_at
from worktrees where not exists (select 1 from deleted_items where kind='worktree' and resource_id=worktrees.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value model.Worktree
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.RepositoryID, &value.Path, &value.Branch, &value.BaseRef, &value.SourceRemote, &value.Lifecycle, &value.CreatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Worktrees = append(out.Worktrees, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = tx.QueryContext(ctx, `select id,workstream_id,title,role,created_by_agent_id,presentation,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at
from agents where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		value, err := scanAgent(rows)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Agents = append(out.Agents, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	agentIndex := make(map[string]int, len(out.Agents))
	for index := range out.Agents {
		agentIndex[out.Agents[index].ID] = index
	}
	assignmentRows, err := tx.QueryContext(ctx, `select agent_id,worktree_id,position,assignment_mode from agent_worktrees order by agent_id,position`)
	if err != nil {
		return out, err
	}
	for assignmentRows.Next() {
		var agentID string
		var assignment model.AgentWorktree
		if err := assignmentRows.Scan(&agentID, &assignment.WorktreeID, &assignment.Position, &assignment.Mode); err != nil {
			_ = assignmentRows.Close()
			return out, err
		}
		if index, ok := agentIndex[agentID]; ok {
			out.Agents[index].Placement.Worktrees = append(out.Agents[index].Placement.Worktrees, assignment)
		}
	}
	if err := assignmentRows.Close(); err != nil {
		return out, err
	}

	messageRows, err := tx.QueryContext(ctx, `select `+agentMessageColumns+` from agent_messages order by created_at,id`)
	if err != nil {
		return out, err
	}
	var messageCandidates []model.AgentMessage
	excludedRuns := make(map[string]bool)
	for messageRows.Next() {
		value, err := scanAgentMessage(messageRows)
		if err != nil {
			_ = messageRows.Close()
			return out, err
		}
		messageCandidates = append(messageCandidates, value)
		_, targetLive := agentIndex[value.TargetAgentID]
		_, senderLive := agentIndex[value.SenderAgentID]
		if !targetLive || value.SenderAgentID != "" && !senderLive {
			excludedRuns[value.RunID] = true
		}
	}
	if err := messageRows.Close(); err != nil {
		return out, err
	}
	messageIDs := make(map[string]bool, len(messageCandidates))
	for _, value := range messageCandidates {
		if excludedRuns[value.RunID] {
			continue
		}
		images, imageErr := loadMessageImages(ctx, tx, value.ID, true)
		if imageErr != nil {
			return out, imageErr
		}
		value.Images = imagePointer(images)
		out.Messages = append(out.Messages, value)
		messageIDs[value.ID] = true
		if value.IdempotencyKey != "" {
			out.MessageIdempotencyKeys[value.ID] = value.IdempotencyKey
		}
	}
	if s.durableStateMessagesRead != nil {
		s.durableStateMessagesRead()
	}
	eventRows, err := tx.QueryContext(ctx, `select `+lifecycleEventColumns+` from lifecycle_events order by created_at,id`)
	if err != nil {
		return out, err
	}
	for eventRows.Next() {
		value, scanErr := scanLifecycleEvent(eventRows)
		if scanErr != nil {
			_ = eventRows.Close()
			return out, scanErr
		}
		_, recipientLive := agentIndex[value.RecipientAgentID]
		_, subjectLive := agentIndex[value.SubjectAgentID]
		if !recipientLive || value.SubjectAgentID != "" && !subjectLive || value.MessageID != "" && !messageIDs[value.MessageID] {
			continue
		}
		out.LifecycleEvents = append(out.LifecycleEvents, value)
	}
	if err := eventRows.Close(); err != nil {
		return out, err
	}
	progressRows, err := tx.QueryContext(ctx, `select `+workProgressColumns+` from work_progress_events order by sequence`)
	if err != nil {
		return out, err
	}
	for progressRows.Next() {
		value, scanErr := scanWorkProgress(progressRows)
		if scanErr != nil {
			_ = progressRows.Close()
			return out, scanErr
		}
		if messageIDs[value.MessageID] {
			out.WorkProgressEvents = append(out.WorkProgressEvents, value)
		}
	}
	if err := progressRows.Close(); err != nil {
		return out, err
	}
	if err := validateDurableMessages(out); err != nil {
		return out, fmt.Errorf("build durable checkpoint graph: %w", err)
	}
	return out, tx.Commit()
}

// RestoreDurableState imports a logical checkpoint into a new, empty store.
func (s *Store) RestoreDurableState(ctx context.Context, state model.DurableState) error {
	if err := validateDurableMessages(state); err != nil {
		return err
	}
	empty, err := s.Empty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("checkpoint restore needs an empty Galpon state directory")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, repository := range state.Repositories {
		if _, err := tx.ExecContext(ctx, `insert into repositories(id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at) values(?,?,?,?,?,?,?,?,?)`,
			repository.ID, repository.Title, repository.SourcePath, repository.FetchURL, repository.MirrorPath, repository.DefaultRemote, repository.PushRemote, repository.DefaultBranch, repository.CreatedAt); err != nil {
			return fmt.Errorf("restore repository %s: %w", repository.ID, err)
		}
		for _, remote := range repository.Remotes {
			if _, err := tx.ExecContext(ctx, `insert into repository_remotes(repository_id,name,fetch_url,push_url,created_at) values(?,?,?,?,?)`, repository.ID, remote.Name, remote.FetchURL, remote.PushURL, repository.CreatedAt); err != nil {
				return fmt.Errorf("restore remote %s for repository %s: %w", remote.Name, repository.ID, err)
			}
		}
	}
	for _, workspace := range state.Workspaces {
		if _, err := tx.ExecContext(ctx, `insert into workstreams(id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at) values(?,?,?,?,?,?,?,?)`,
			workspace.ID, workspace.Title, workspace.Status, workspace.Renderer, workspace.RendererContext, workspace.RendererID, workspace.CreatedAt, workspace.UpdatedAt); err != nil {
			return fmt.Errorf("restore workspace %s: %w", workspace.ID, err)
		}
	}
	for _, worktree := range state.Worktrees {
		if err := putWorktree(ctx, tx, worktree); err != nil {
			return fmt.Errorf("restore worktree %s: %w", worktree.ID, err)
		}
	}
	for _, agent := range state.Agents {
		if _, err := tx.ExecContext(ctx, `insert into agents(id,workstream_id,title,role,created_by_agent_id,presentation,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			agent.ID, agent.WorkspaceID, agent.Title, agent.Role, agent.CreatedByAgentID, normalizedPresentation(agent.Presentation), agent.ContextAgentID, agent.Placement.Type, agent.Placement.CWD, agent.Placement.PrimaryWorktreeID, agent.Kind, agent.Status, agent.SessionID, agent.SessionPath, agent.Renderer, agent.RendererContext, agent.RendererID, agent.RuntimeID, agent.LastError, agent.CreatedAt, agent.UpdatedAt); err != nil {
			return fmt.Errorf("restore agent %s: %w", agent.ID, err)
		}
		for _, assignment := range agent.Placement.Worktrees {
			if _, err := tx.ExecContext(ctx, `insert into agent_worktrees(agent_id,worktree_id,position,assignment_mode) values(?,?,?,?)`, agent.ID, assignment.WorktreeID, assignment.Position, assignment.Mode); err != nil {
				return fmt.Errorf("restore placement for agent %s: %w", agent.ID, err)
			}
		}
	}
	for _, message := range state.Messages {
		message = normalizeAgentMessage(message)
		message.IdempotencyKey = state.MessageIdempotencyKeys[message.ID]
		if _, err := tx.ExecContext(ctx, `insert into agent_messages(`+agentMessageColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, agentMessageValues(message)...); err != nil {
			return fmt.Errorf("restore agent message %s: %w", message.ID, err)
		}
		if err := putMessageImages(ctx, tx, message.ID, messageImageValues(message.Images), message.CreatedAt); err != nil {
			return fmt.Errorf("restore images for agent message %s: %w", message.ID, err)
		}
	}
	for _, progress := range state.WorkProgressEvents {
		milestones, _ := json.Marshal(progress.Milestones)
		counts, _ := json.Marshal(progress.Counts)
		if _, err := tx.ExecContext(ctx, `insert into work_progress_events(message_id,event_id,runtime_id,attempt,version,phase,summary,milestones,blocker,counts,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, progress.MessageID, progress.EventID, progress.RuntimeID, progress.Attempt, progress.Version, progress.Phase, progress.Summary, string(milestones), progress.Blocker, string(counts), progress.CreatedAt); err != nil {
			return fmt.Errorf("restore work progress %s: %w", progress.EventID, err)
		}
	}
	for _, event := range state.LifecycleEvents {
		if event.ID == "" || event.EventType == "" || event.RecipientAgentID == "" || (event.Status != "pending" && event.Status != "delivered") {
			return fmt.Errorf("restore lifecycle event has invalid fields")
		}
		if _, err := tx.ExecContext(ctx, `insert into lifecycle_events(`+lifecycleEventColumns+`) values(?,?,?,?,?,?,?,?,?,?)`, event.ID, event.EventType, event.SubjectAgentID, event.RecipientAgentID, event.MessageID, event.Payload, event.CoalesceKey, event.Status, event.CreatedAt, event.DeliveredAt); err != nil {
			return fmt.Errorf("restore lifecycle event %s: %w", event.ID, err)
		}
	}
	return tx.Commit()
}

func validateDurableMessages(state model.DurableState) error {
	agents := make(map[string]bool, len(state.Agents))
	for _, agent := range state.Agents {
		agents[agent.ID] = true
	}
	messages := make(map[string]model.AgentMessage, len(state.Messages))
	for _, input := range state.Messages {
		message := normalizeAgentMessage(input)
		if message.ID == "" || messages[message.ID].ID != "" {
			return fmt.Errorf("checkpoint has an empty or duplicate agent message ID")
		}
		if !agents[message.TargetAgentID] {
			return fmt.Errorf("checkpoint message %s has an unknown target agent", message.ID)
		}
		if message.SenderAgentID != "" && !agents[message.SenderAgentID] {
			return fmt.Errorf("checkpoint message %s has an unknown sender agent", message.ID)
		}
		if message.Kind != "request" && message.Kind != "result" {
			return fmt.Errorf("checkpoint message %s has invalid kind %q", message.ID, message.Kind)
		}
		if message.Act != "request" && message.Act != "query" && message.Act != "inform" && message.Act != "done" {
			return fmt.Errorf("checkpoint message %s has invalid act %q", message.ID, message.Act)
		}
		if message.ResultMode != "join" && message.ResultMode != "notify" && message.ResultMode != "none" {
			return fmt.Errorf("checkpoint message %s has invalid result mode %q", message.ID, message.ResultMode)
		}
		if message.Kind == "result" && (message.Act != "done" || message.ResultMode != "none") {
			return fmt.Errorf("checkpoint result %s has invalid protocol fields", message.ID)
		}
		if message.Kind == "request" && message.Act == "inform" && message.ResultMode != "none" {
			return fmt.Errorf("checkpoint inform message %s expects a result", message.ID)
		}
		if message.Kind == "request" && message.Act != "inform" && message.ResultMode == "none" {
			return fmt.Errorf("checkpoint message %s suppresses a required result", message.ID)
		}
		if message.Kind == "request" && message.ResultMode == "join" && message.ParentMessageID == "" {
			return fmt.Errorf("checkpoint joined message %s has no parent", message.ID)
		}
		if message.Status != "queued" && message.Status != "delivered" && message.Status != "completed" && message.Status != "failed" {
			return fmt.Errorf("checkpoint message %s has invalid status %q", message.ID, message.Status)
		}
		if message.Depth < 0 || message.Depth > 16 {
			return fmt.Errorf("checkpoint message %s has invalid orchestration depth", message.ID)
		}
		images := messageImageValues(message.Images)
		if len(images) > 4 {
			return fmt.Errorf("checkpoint message %s has too many images", message.ID)
		}
		imageTotal := 0
		for _, image := range images {
			data, err := base64.StdEncoding.DecodeString(image.Data)
			if err != nil || image.ID == "" || int64(len(data)) != image.Size || image.Size <= 0 || image.Size > 8<<20 {
				return fmt.Errorf("checkpoint message %s has an invalid image", message.ID)
			}
			imageTotal += len(data)
		}
		if imageTotal > 20<<20 {
			return fmt.Errorf("checkpoint message %s image data is too large", message.ID)
		}
		if message.NotificationState != "none" && message.NotificationState != "pending" && message.NotificationState != "delivered" && message.NotificationState != "suppressed" && message.NotificationState != "completed" {
			return fmt.Errorf("checkpoint message %s has invalid notification state", message.ID)
		}
		messages[message.ID] = message
	}
	for _, message := range messages {
		if message.RootMessageID == "" || message.RunID == "" {
			return fmt.Errorf("checkpoint message %s has incomplete causal metadata", message.ID)
		}
		root, ok := messages[message.RootMessageID]
		if !ok {
			return fmt.Errorf("checkpoint message %s has unknown root %s", message.ID, message.RootMessageID)
		}
		if root.RootMessageID != root.ID || root.RunID != message.RunID {
			return fmt.Errorf("checkpoint message %s has an inconsistent causal root", message.ID)
		}
		if message.Kind == "request" && message.ReplyTo != "" {
			return fmt.Errorf("checkpoint request %s has an unexpected reply target", message.ID)
		}
		if message.ReplyTo != "" {
			reply, ok := messages[message.ReplyTo]
			if !ok {
				return fmt.Errorf("checkpoint message %s has unknown reply target %s", message.ID, message.ReplyTo)
			}
			if message.Kind == "result" && reply.Kind != "request" {
				return fmt.Errorf("checkpoint result %s does not reply to a request", message.ID)
			}
			if message.ParentMessageID != "" && message.ParentMessageID != message.ReplyTo {
				return fmt.Errorf("checkpoint result %s has different parent and reply targets", message.ID)
			}
		}
		if message.ParentMessageID == "" {
			if message.Kind == "request" && message.Depth != 0 {
				return fmt.Errorf("checkpoint root message %s has nonzero depth", message.ID)
			}
			continue
		}
		parent, ok := messages[message.ParentMessageID]
		if !ok {
			return fmt.Errorf("checkpoint message %s has unknown parent %s", message.ID, message.ParentMessageID)
		}
		if parent.RootMessageID != message.RootMessageID || parent.RunID != message.RunID {
			return fmt.Errorf("checkpoint message %s does not inherit its parent cause", message.ID)
		}
		wantDepth := parent.Depth + 1
		if message.Kind == "result" {
			wantDepth = parent.Depth
		}
		if message.Depth != wantDepth {
			return fmt.Errorf("checkpoint message %s has invalid causal depth", message.ID)
		}
	}
	if len(state.WorkProgressEvents) > WorkProgressTotalLimit {
		return fmt.Errorf("checkpoint has too many work progress events")
	}
	progressIDs := make(map[string]bool, len(state.WorkProgressEvents))
	progressPerMessage := make(map[string]int)
	for _, progress := range state.WorkProgressEvents {
		key := progress.MessageID + "\x00" + progress.EventID
		validated, validationErr := model.ValidateWorkProgress(progress)
		if messages[progress.MessageID].ID == "" || messages[progress.MessageID].Kind != "request" || strings.TrimSpace(progress.RuntimeID) == "" || progressIDs[key] || progress.Attempt < 1 || progress.CreatedAt <= 0 || validationErr != nil || !reflect.DeepEqual(validated, progress) {
			return fmt.Errorf("checkpoint has invalid work progress")
		}
		progressIDs[key] = true
		progressPerMessage[progress.MessageID]++
		if progressPerMessage[progress.MessageID] > WorkProgressPerMessageLimit {
			return fmt.Errorf("checkpoint has too many work progress events for one message")
		}
	}
	events := make(map[string]bool, len(state.LifecycleEvents))
	for _, event := range state.LifecycleEvents {
		if event.ID == "" || events[event.ID] {
			return fmt.Errorf("checkpoint has an empty or duplicate lifecycle event ID")
		}
		events[event.ID] = true
		if event.EventType == "" || !agents[event.RecipientAgentID] {
			return fmt.Errorf("checkpoint lifecycle event %s has invalid type or recipient", event.ID)
		}
		if event.SubjectAgentID != "" && !agents[event.SubjectAgentID] {
			return fmt.Errorf("checkpoint lifecycle event %s has an unknown subject agent", event.ID)
		}
		if event.MessageID != "" {
			if _, ok := messages[event.MessageID]; !ok {
				return fmt.Errorf("checkpoint lifecycle event %s has an unknown message", event.ID)
			}
		}
		if event.Status != "pending" && event.Status != "delivered" {
			return fmt.Errorf("checkpoint lifecycle event %s has invalid status", event.ID)
		}
	}
	return nil
}

func (s *Store) Empty(ctx context.Context) (bool, error) {
	for _, table := range []string{"repositories", "workstreams", "worktrees", "agents", "agent_messages", "work_progress_events", "image_blobs", "lifecycle_events", "deleted_items"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `select count(*) from `+table).Scan(&count); err != nil {
			return false, err
		}
		if count != 0 {
			return false, nil
		}
	}
	return true, nil
}
