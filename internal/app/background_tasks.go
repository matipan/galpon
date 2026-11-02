package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// backgroundTaskReportLife is how long a background task report counts. A Pi
// runtime sends its counts with each delegated-status request, at least every
// 15 seconds, and at once when a task starts or ends.
const backgroundTaskReportLife = time.Minute

// backgroundTaskReport is the last background task count of an agent runtime.
// The daemon keeps it only in memory: the runtime sends it again within
// seconds after a daemon restart.
type backgroundTaskReport struct {
	runtimeID string
	tasks     model.BackgroundTasks
	at        time.Time
}

// ReportBackgroundTasks keeps the background tasks that the runtime runtimeID
// of an agent reported. The caller checks that the runtime belongs to the
// agent.
func (a *App) ReportBackgroundTasks(agentID, runtimeID string, tasks model.BackgroundTasks) {
	a.backgroundTaskMu.Lock()
	defer a.backgroundTaskMu.Unlock()
	now := time.Now()
	for id, report := range a.backgroundTaskReports {
		if now.Sub(report.at) >= backgroundTaskReportLife {
			delete(a.backgroundTaskReports, id)
		}
	}
	if tasks.Commands <= 0 && tasks.Monitors <= 0 {
		delete(a.backgroundTaskReports, agentID)
		return
	}
	if a.backgroundTaskReports == nil {
		a.backgroundTaskReports = make(map[string]backgroundTaskReport)
	}
	a.backgroundTaskReports[agentID] = backgroundTaskReport{runtimeID: runtimeID, tasks: tasks, at: now}
}

// BackgroundTasks returns the background tasks that run for each agent, as
// the current runtime of the agent reported them. A report from an earlier
// runtime, or one older than backgroundTaskReportLife, does not count. Agents
// without background tasks are not in the result.
func (a *App) BackgroundTasks(ctx context.Context, agentIDs []string) (map[string]model.BackgroundTasks, error) {
	reports := make(map[string]backgroundTaskReport, len(agentIDs))
	a.backgroundTaskMu.Lock()
	for _, id := range agentIDs {
		if report, ok := a.backgroundTaskReports[id]; ok && time.Since(report.at) < backgroundTaskReportLife {
			reports[id] = report
		}
	}
	a.backgroundTaskMu.Unlock()
	out := make(map[string]model.BackgroundTasks, len(reports))
	for id, report := range reports {
		agent, err := a.Store.Agent(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if agent.RuntimeID != "" && agent.RuntimeID == report.runtimeID {
			out[id] = report.tasks
		}
	}
	return out, nil
}
