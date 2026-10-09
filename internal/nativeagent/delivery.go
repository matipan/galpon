package nativeagent

import (
	"context"
	"strings"
)

const maxExpiredDeliveries = 128

// A foreground writer belongs to the user. If a channel or queue cannot confirm
// withdrawal, retain ownership until saved input arrives or that writer exits.
func (c *runtimeController) expireDelivery(ctx context.Context, job *runtimeJob, failure string) {
	if job.Token != "" && !job.Terminal && c.driver != nil {
		withdrawn, _ := c.driver.Withdraw(ctx, job.Token)
		if !withdrawn {
			if !c.background {
				job.Blocked = failure + " Galpon has paused incoming work and kept this terminal open. Close the agent to fail this delivery, or enable its native channel to continue it."
				return
			}
			c.registered = false
			c.driver.Close()
			c.stopping = true
		}
	}
	c.rememberExpired(job.Token)
	job.Terminal, job.Final, job.Failure = true, "", failure
}

// Run calls this only after the native writer is stopped, while this runtime
// still owns its operation attempts. A crash leaves Blocked in the journal so
// the next fenced runtime can fail the delivery instead of submitting it again.
func (c *runtimeController) finishBlocked(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := 0; index < len(c.jobs); {
		job := c.jobs[index]
		if job.Blocked == "" {
			index++
			continue
		}
		c.rememberExpired(job.Token)
		job.Terminal, job.Final, job.Failure = true, "", "The native writer stopped before the blocked delivery was confirmed"
		if err := c.save(); err != nil {
			return err
		}
		if job.Operation.ID != "" {
			if _, err := c.client.SettleCoordinationOperation(ctx, c.agent.ID, job.Operation.ID, c.runtimeID, job.Operation.Attempt, "", job.Failure); err != nil {
				return err
			}
		}
		c.removeJob(index)
		if err := c.save(); err != nil {
			return err
		}
	}
	return nil
}

func (c *runtimeController) rememberExpired(token string) {
	if token == "" {
		return
	}
	for _, existing := range c.expiredTokens {
		if existing == token {
			return
		}
	}
	c.expiredTokens = append(c.expiredTokens, token)
	if len(c.expiredTokens) > maxExpiredDeliveries {
		c.expiredTokens = c.expiredTokens[len(c.expiredTokens)-maxExpiredDeliveries:]
	}
}

func (c *runtimeController) hasExpiredDelivery(input string) bool {
	// A new delivery can quote an expired prompt in its user-supplied body.
	for _, job := range c.jobs {
		if !job.Terminal && job.Token != "" && job.Prompt != "" && strings.Contains(input, job.Prompt) {
			return false
		}
	}
	for _, token := range c.expiredTokens {
		if strings.Contains(input, deliveryMarker(token)) {
			return true
		}
	}
	return false
}
