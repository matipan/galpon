package app

import (
	"context"
	"sync"
	"time"
)

const (
	coordinationWaitTick   = 250 * time.Millisecond
	coordinationWaitRescan = 5 * time.Second
	coordinationWaitMax    = 25 * time.Second
)

// CoordinationWake tells an idle runtime whether it should run its normal claim
// sequence. It does not claim, lease, or acknowledge work.
type CoordinationWake struct {
	Fingerprint string `json:"fingerprint"`
	Changed     bool   `json:"changed"`
}

type coordinationWaiter struct {
	agentID string
	since   string
	ready   chan string
}

// coordinationWaitHub replaces one claim poll per idle runtime with one shared
// check. It reads coordination tables only after a database change, and at a
// fixed interval for time-based lease expiry.
type coordinationWaitHub struct {
	mu      sync.Mutex
	waiters map[*coordinationWaiter]struct{}
	kick    chan struct{}
	running bool
	force   bool
}

func (a *App) WaitCoordinationChange(ctx context.Context, agentID, since string, timeout time.Duration) (CoordinationWake, error) {
	if timeout <= 0 || timeout > coordinationWaitMax {
		timeout = coordinationWaitMax
	}
	waiter := &coordinationWaiter{agentID: agentID, since: since, ready: make(chan string, 1)}
	a.registerCoordinationWaiter(waiter)
	defer a.unregisterCoordinationWaiter(waiter)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var stopping <-chan struct{}
	if a.backgroundContext != nil {
		stopping = a.backgroundContext.Done()
	}
	select {
	case fingerprint := <-waiter.ready:
		return CoordinationWake{Fingerprint: fingerprint, Changed: true}, nil
	case <-timer.C:
		return CoordinationWake{Fingerprint: since}, nil
	case <-stopping:
		return CoordinationWake{Fingerprint: since}, nil
	case <-ctx.Done():
		return CoordinationWake{}, ctx.Err()
	}
}

func (a *App) registerCoordinationWaiter(waiter *coordinationWaiter) {
	hub := &a.coordinationWaits
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.waiters == nil {
		hub.waiters = make(map[*coordinationWaiter]struct{})
		hub.kick = make(chan struct{}, 1)
	}
	hub.waiters[waiter] = struct{}{}
	// Compare the new waiter with current state at once. This closes the gap
	// between the runtime's last observation and this registration.
	hub.force = true
	select {
	case hub.kick <- struct{}{}:
	default:
	}
	if !hub.running {
		hub.running = true
		go a.runCoordinationWaitHub()
	}
}

func (a *App) unregisterCoordinationWaiter(waiter *coordinationWaiter) {
	hub := &a.coordinationWaits
	hub.mu.Lock()
	delete(hub.waiters, waiter)
	hub.mu.Unlock()
}

func (a *App) runCoordinationWaitHub() {
	hub := &a.coordinationWaits
	ctx := a.backgroundContext
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(coordinationWaitTick)
	defer ticker.Stop()
	mark, scanned := "", time.Time{}
	for {
		select {
		case <-ctx.Done():
			hub.mu.Lock()
			hub.running = false
			hub.mu.Unlock()
			return
		case <-ticker.C:
		case <-hub.kick:
		}
		hub.mu.Lock()
		if len(hub.waiters) == 0 {
			hub.running = false
			hub.mu.Unlock()
			return
		}
		force := hub.force
		hub.force = false
		hub.mu.Unlock()
		// Read the mark before the fingerprints. A change during the scan then
		// causes one more scan instead of a missed wake.
		next, err := a.Store.CoordinationChangeMark(ctx)
		if err == nil && !force && next == mark && time.Since(scanned) < coordinationWaitRescan {
			continue
		}
		global, agents, err := a.Store.CoordinationWakeFingerprints(ctx, time.Now().UnixMilli())
		if err != nil {
			// Waiters time out and their runtimes run a normal claim.
			continue
		}
		mark, scanned = next, time.Now()
		hub.mu.Lock()
		for waiter := range hub.waiters {
			fingerprint := global + "|" + agents[waiter.agentID]
			if fingerprint == waiter.since {
				continue
			}
			waiter.ready <- fingerprint
			delete(hub.waiters, waiter)
		}
		hub.mu.Unlock()
	}
}
