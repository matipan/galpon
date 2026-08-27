package store

import (
	"fmt"
	"testing"
)

func TestReconcileAgentOperationOwnershipUsesEveryAuthoritativeNonterminalState(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	states := []string{"ready", "claimed", "running", "waiting", "settling", "settled", "failed", "canceled", "expired"}
	for index, state := range states {
		id := "ownership-" + state
		if _, err := s.db.Exec(`insert into agent_operations(id,agent_id,kind,state,causal_run_id,created_at,updated_at,protocol_generation) values(?, 'captain', 'direct', ?, ?, ?, ?, 2)`, id, state, id, index+1, index+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`insert into agent_operations(id,agent_id,kind,state,causal_run_id,created_at,updated_at,protocol_generation) values('foreign-ready', 'worker', 'direct', 'ready', 'foreign-ready', 20, 20, 2)`); err != nil {
		t.Fatal(err)
	}
	input := []string{"ownership-waiting", "ownership-ready", "ownership-expired", "missing", "ownership-claimed", "foreign-ready", "ownership-running", "ownership-settling", "ownership-settled"}
	owned, err := s.ReconcileAgentOperationOwnership(t.Context(), "captain", input)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(owned)
	want := "[ownership-waiting ownership-ready ownership-claimed ownership-running ownership-settling]"
	if got != want {
		t.Fatalf("owned operations = %s, want %s", got, want)
	}
}
