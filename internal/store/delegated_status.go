package store

import "context"

// ActiveDelegatedAgentCount returns the number of active background descendants
// below one agent. Foreground descendants remain in the lineage traversal, but
// they are not themselves counted as delegated agents.
func (s *Store) ActiveDelegatedAgentCount(ctx context.Context, creatorID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `with recursive descendants(id) as (
  select id from agents
  where created_by_agent_id=?
    and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)
  union
  select agents.id from agents join descendants on agents.created_by_agent_id=descendants.id
  where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)
)
select count(*) from agents join descendants on descendants.id=agents.id
where agents.presentation='background' and agents.status in ('starting','running')`, creatorID).Scan(&count)
	return count, err
}
