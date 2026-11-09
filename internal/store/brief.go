package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/matipan/galpon/internal/model"
)

// AgentBrief gives the last message to an agent and the last text that the
// agent wrote, from its conversation events.
func (s *Store) AgentBrief(ctx context.Context, agentID string) (model.AgentBrief, error) {
	var brief model.AgentBrief
	err := s.db.QueryRowContext(ctx, `select content,created_at from conversation_events
		where agent_id=? and kind='user_message' and is_delta=0 and content<>'' order by sequence desc limit 1`, agentID).Scan(&brief.Prompt, &brief.PromptAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.AgentBrief{}, err
	}
	err = s.db.QueryRowContext(ctx, `select content,created_at from conversation_events
		where agent_id=? and kind='assistant_message_end' and content<>'' order by sequence desc limit 1`, agentID).Scan(&brief.Reply, &brief.ReplyAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.AgentBrief{}, err
	}
	return brief, nil
}
