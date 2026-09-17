package store

import (
	"context"
	"fmt"

	"github.com/matipan/galpon/internal/model"
)

func (s *Store) ReservePlanLaunch(ctx context.Context, value model.PlanLaunch) (model.PlanLaunch, error) {
	_, err := s.db.ExecContext(ctx, `insert or ignore into plan_launches(source_agent_id,revision_id,plan_hash,agent_id,created) values(?,?,?,?,0)`, value.SourceAgentID, value.RevisionID, value.PlanHash, value.AgentID)
	if err != nil {
		return model.PlanLaunch{}, err
	}
	var stored model.PlanLaunch
	err = s.db.QueryRowContext(ctx, `select source_agent_id,revision_id,plan_hash,agent_id,created from plan_launches where source_agent_id=? and revision_id=?`, value.SourceAgentID, value.RevisionID).Scan(&stored.SourceAgentID, &stored.RevisionID, &stored.PlanHash, &stored.AgentID, &stored.Created)
	if err == nil && stored.PlanHash != value.PlanHash {
		err = fmt.Errorf("the saved plan revision has different content")
	}
	return stored, err
}

func (s *Store) PutPlanAgent(ctx context.Context, agent model.Agent, created []model.Worktree, launch model.PlanLaunch) error {
	return s.putAgent(ctx, agent, created, &launch)
}
