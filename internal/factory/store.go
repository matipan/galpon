package factory

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(stateDir string) (*Store, error) {
	path := filepath.Join(stateDir, "factory.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize Factory database: %w", err)
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(ctx context.Context, in CreateRequest) (WorkOrder, error) {
	now := time.Now().UnixMilli()
	w := WorkOrder{ID: uuid.NewString(), Title: in.Title, Request: in.Request, IssueURL: in.IssueURL, RepositoryID: in.RepositoryID, RepositoryPath: in.RepositoryPath, WorkspaceID: in.WorkspaceID, BaseRef: in.BaseRef, Stage: StageIntake, Status: "active", CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_orders(id,title,request,issue_url,repository_id,repository_path,workspace_id,base_ref,stage,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.Title, w.Request, w.IssueURL, w.RepositoryID, w.RepositoryPath, w.WorkspaceID, w.BaseRef, w.Stage, w.Status, now, now)
	if err != nil {
		return WorkOrder{}, err
	}
	_ = s.Event(ctx, w.ID, "created", "Feature added to Factory")
	return w, nil
}

func scanWorkOrder(row interface{ Scan(...any) error }) (WorkOrder, error) {
	var w WorkOrder
	err := row.Scan(&w.ID, &w.Title, &w.Request, &w.IssueURL, &w.RepositoryID, &w.RepositoryPath, &w.WorkspaceID, &w.BaseRef, &w.Stage, &w.Status, &w.Plan, &w.DeveloperID, &w.Commit, &w.PRURL, &w.PRNumber, &w.CIStatus, &w.LastError, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

const workColumns = `id,title,request,issue_url,repository_id,repository_path,workspace_id,base_ref,stage,status,plan,developer_id,commit_sha,pr_url,pr_number,ci_status,last_error,created_at,updated_at`

func (s *Store) Get(ctx context.Context, id string) (WorkOrder, error) {
	return scanWorkOrder(s.db.QueryRowContext(ctx, `SELECT `+workColumns+` FROM work_orders WHERE id=?`, id))
}
func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM work_orders WHERE id=?`, id)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) List(ctx context.Context) ([]WorkOrder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workColumns+` FROM work_orders ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []WorkOrder
	for rows.Next() {
		w, e := scanWorkOrder(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (s *Store) SetStage(ctx context.Context, id string, stage Stage, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_orders SET stage=?,status=?,last_error='',updated_at=? WHERE id=?`, stage, status, time.Now().UnixMilli(), id)
	return err
}
func (s *Store) SetPlan(ctx context.Context, id, plan string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_orders SET plan=?,updated_at=? WHERE id=?`, plan, time.Now().UnixMilli(), id)
	return err
}
func (s *Store) SetRequest(ctx context.Context, id, request string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_orders SET request=?,status='active',last_error='',updated_at=? WHERE id=?`, request, time.Now().UnixMilli(), id)
	return err
}
func (s *Store) SetDeveloper(ctx context.Context, id, agent string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_orders SET developer_id=?,updated_at=? WHERE id=?`, agent, time.Now().UnixMilli(), id)
	return err
}
func (s *Store) SetCommit(ctx context.Context, id, commit string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_orders SET commit_sha=?,updated_at=? WHERE id=?`, commit, time.Now().UnixMilli(), id)
	return err
}
func (s *Store) SetPR(ctx context.Context, id, url string, number int, ci string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_orders SET pr_url=?,pr_number=?,ci_status=?,updated_at=? WHERE id=?`, url, number, ci, time.Now().UnixMilli(), id)
	return err
}
func (s *Store) SetCI(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_orders SET ci_status=?,updated_at=? WHERE id=?`, status, time.Now().UnixMilli(), id)
	return err
}
func (s *Store) Fail(ctx context.Context, id string, err error) error {
	_, e := s.db.ExecContext(ctx, `UPDATE work_orders SET status='blocked',last_error=?,updated_at=? WHERE id=?`, err.Error(), time.Now().UnixMilli(), id)
	return e
}

func (s *Store) Event(ctx context.Context, id, kind, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events(id,work_order_id,kind,message,created_at) VALUES(?,?,?,?,?)`, uuid.NewString(), id, kind, message, time.Now().UnixMilli())
	return err
}
func (s *Store) Events(ctx context.Context, id string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,work_order_id,kind,message,created_at FROM events WHERE work_order_id=? ORDER BY created_at DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.WorkOrderID, &e.Kind, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) Runs(ctx context.Context, id string) ([]AgentRun, error) {
	return s.queryRuns(ctx, `SELECT id,work_order_id,agent_id,kind,commit_sha,status,result,created_at,updated_at FROM agent_runs WHERE work_order_id=? ORDER BY created_at, rowid`, id)
}
func (s *Store) RunningRuns(ctx context.Context) ([]AgentRun, error) {
	return s.queryRuns(ctx, `SELECT id,work_order_id,agent_id,kind,commit_sha,status,result,created_at,updated_at FROM agent_runs WHERE status='running' ORDER BY created_at, rowid`)
}
func (s *Store) queryRuns(ctx context.Context, query string, args ...any) ([]AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AgentRun
	for rows.Next() {
		var run AgentRun
		if err := rows.Scan(&run.ID, &run.WorkOrderID, &run.AgentID, &run.Kind, &run.Commit, &run.Status, &run.Result, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
func (s *Store) PutRun(ctx context.Context, r AgentRun) error {
	now := time.Now().UnixMilli()
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_runs(id,work_order_id,agent_id,kind,commit_sha,status,result,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(work_order_id,kind,commit_sha) DO UPDATE SET agent_id=excluded.agent_id,status=excluded.status,result=excluded.result,updated_at=excluded.updated_at`, r.ID, r.WorkOrderID, r.AgentID, r.Kind, r.Commit, r.Status, r.Result, now, now)
	return err
}
