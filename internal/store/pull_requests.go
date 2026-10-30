package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/matipan/galpon/internal/model"
)

// PullRequestCreator records that the conversation of an agent ran gh pr
// create and got the URL of a pull request. Repository is lowercase.
type PullRequestCreator struct {
	Repository string
	Number     int
	AgentID    string
	// CreatedAt is the time of the saved result in milliseconds, or 0 when the
	// conversation has no time for it.
	CreatedAt int64
}

// ConversationScan is how far the daemon has read one conversation file.
type ConversationScan struct {
	ReadBytes int64
	Pending   []string
}

// GitHubPullRequests returns the last GitHub sync without Galpon links.
func (s *Store) GitHubPullRequests(ctx context.Context) (model.PullRequests, error) {
	var out model.PullRequests
	var data string
	err := s.db.QueryRowContext(ctx, `select login,fetched_at,error,pull_requests from github_sync where id=1`).Scan(&out.Login, &out.FetchedAt, &out.Error, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return model.PullRequests{}, nil
	}
	if err != nil {
		return model.PullRequests{}, err
	}
	if err := json.Unmarshal([]byte(data), &out.PullRequests); err != nil {
		return model.PullRequests{}, err
	}
	return out, nil
}

// SaveGitHubPullRequests replaces the cached pull requests and clears the last
// sync error.
func (s *Store) SaveGitHubPullRequests(ctx context.Context, login string, fetchedAt int64, pullRequests []model.PullRequest) error {
	if pullRequests == nil {
		pullRequests = []model.PullRequest{}
	}
	data, err := json.Marshal(pullRequests)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into github_sync(id,login,fetched_at,error,pull_requests) values(1,?,?,'',?)
on conflict(id) do update set login=excluded.login,fetched_at=excluded.fetched_at,error='',pull_requests=excluded.pull_requests`, login, fetchedAt, string(data))
	return err
}

// SaveGitHubSyncError keeps the cached pull requests and records why the last
// sync failed.
func (s *Store) SaveGitHubSyncError(ctx context.Context, message string) error {
	_, err := s.db.ExecContext(ctx, `insert into github_sync(id,error) values(1,?) on conflict(id) do update set error=excluded.error`, message)
	return err
}

func (s *Store) ConversationScans(ctx context.Context) (map[string]ConversationScan, error) {
	rows, err := s.db.QueryContext(ctx, `select path,read_bytes,pending from conversation_scans`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ConversationScan{}
	for rows.Next() {
		var path, pending string
		var scan ConversationScan
		if err := rows.Scan(&path, &scan.ReadBytes, &pending); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(pending), &scan.Pending); err != nil {
			return nil, err
		}
		out[path] = scan
	}
	return out, rows.Err()
}

// RecordConversationScans saves read positions and the creators that they
// found in one transaction, so a failed save reads the same lines again. It
// ignores creators whose agent no longer exists.
func (s *Store) RecordConversationScans(ctx context.Context, scans map[string]ConversationScan, removed []string, creators []PullRequestCreator) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for path, scan := range scans {
		pending := scan.Pending
		if pending == nil {
			pending = []string{}
		}
		data, err := json.Marshal(pending)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into conversation_scans(path,read_bytes,pending) values(?,?,?)
on conflict(path) do update set read_bytes=excluded.read_bytes,pending=excluded.pending`, path, scan.ReadBytes, string(data)); err != nil {
			return err
		}
	}
	for _, path := range removed {
		if _, err := tx.ExecContext(ctx, `delete from conversation_scans where path=?`, path); err != nil {
			return err
		}
	}
	for _, creator := range creators {
		// A copied conversation, such as a fork, repeats the original result.
		// Keep the earliest known time for each agent.
		if _, err := tx.ExecContext(ctx, `insert into pull_request_creators(repository,number,agent_id,created_at)
select ?,?,?,? where exists(select 1 from agents where id=?)
on conflict(repository,number,agent_id) do update set created_at=case
  when pull_request_creators.created_at=0 then excluded.created_at
  when excluded.created_at=0 then pull_request_creators.created_at
  else min(pull_request_creators.created_at,excluded.created_at) end`,
			creator.Repository, creator.Number, creator.AgentID, creator.CreatedAt, creator.AgentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PullRequestCreators returns the creators of each pull request by
// model.PullRequestKey.
func (s *Store) PullRequestCreators(ctx context.Context) (map[string][]PullRequestCreator, error) {
	rows, err := s.db.QueryContext(ctx, `select repository,number,agent_id,created_at from pull_request_creators`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]PullRequestCreator{}
	for rows.Next() {
		var creator PullRequestCreator
		if err := rows.Scan(&creator.Repository, &creator.Number, &creator.AgentID, &creator.CreatedAt); err != nil {
			return nil, err
		}
		key := model.PullRequestKey(creator.Repository, creator.Number)
		out[key] = append(out[key], creator)
	}
	return out, rows.Err()
}
