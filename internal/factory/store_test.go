package factory

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStorePersistsWorkOrderRunsAndEvents(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", RepositoryPath: "/repo", WorkspaceID: "workspace", BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if order.Stage != StageIntake || order.Status != "active" {
		t.Fatalf("unexpected order: %#v", order)
	}
	if err := store.SetPlan(ctx, order.ID, "A plan"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStage(ctx, order.ID, StagePlanApproval, "waiting"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: "agent", Kind: "planner", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: "agent", Kind: "planner", Status: "completed", Result: "A plan"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	got, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan != "A plan" || got.Stage != StagePlanApproval {
		t.Fatalf("state not durable: %#v", got)
	}
	runs, err := store.Runs(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "completed" {
		t.Fatalf("unexpected runs: %#v", runs)
	}
	events, err := store.Events(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected durable event")
	}
	if filepath.Base(filepath.Join(dir, "factory.db")) != "factory.db" {
		t.Fatal("bad database path")
	}
}

func TestIndependentFactoryDatabases(t *testing.T) {
	ctx := context.Background()
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err = a.Create(ctx, CreateRequest{Title: "Only A", Request: "x", RepositoryID: "r", WorkspaceID: "w"}); err != nil {
		t.Fatal(err)
	}
	orders, err := b.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 0 {
		t.Fatalf("database leaked state: %#v", orders)
	}
}
