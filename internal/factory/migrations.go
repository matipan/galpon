package factory

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS work_orders (
 id TEXT PRIMARY KEY, title TEXT NOT NULL, request TEXT NOT NULL, issue_url TEXT NOT NULL DEFAULT '',
 repository_id TEXT NOT NULL, repository_path TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL,
 base_ref TEXT NOT NULL DEFAULT '', stage TEXT NOT NULL, status TEXT NOT NULL,
 plan TEXT NOT NULL DEFAULT '', developer_id TEXT NOT NULL DEFAULT '', commit_sha TEXT NOT NULL DEFAULT '',
 pr_url TEXT NOT NULL DEFAULT '', pr_number INTEGER NOT NULL DEFAULT 0, ci_status TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_runs (
 id TEXT PRIMARY KEY, work_order_id TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL, kind TEXT NOT NULL, commit_sha TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
 result TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(work_order_id, kind, commit_sha)
);
CREATE TABLE IF NOT EXISTS events (
 id TEXT PRIMARY KEY, work_order_id TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, message TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS factory_events_order ON events(work_order_id, created_at DESC);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
PRAGMA user_version=1;
`
