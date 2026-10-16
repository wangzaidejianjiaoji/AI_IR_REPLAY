package llm

import (
	"context"
	"sync/atomic"
)

type reviewSessionKey struct{}
type reviewSession struct{ downgraded atomic.Bool }

// WithReviewSession starts routing for one PR. A downgrade lasts until this
// session ends; provider cooldowns remain shared by the client and checkpoints.
func WithReviewSession(ctx context.Context) context.Context {
	return context.WithValue(ctx, reviewSessionKey{}, &reviewSession{})
}

func sessionDowngraded(ctx context.Context) bool {
	session, ok := ctx.Value(reviewSessionKey{}).(*reviewSession)
	return ok && session.downgraded.Load()
}

func downgradeSession(ctx context.Context) {
	if session, ok := ctx.Value(reviewSessionKey{}).(*reviewSession); ok {
		session.downgraded.Store(true)
	}
}

// Availability checks known provider cooldowns without issuing a model request.
// Unknown availability is verified by the first actual Chat request.
func (c *Client) Availability(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	primary := c.blockedQuota(ctx)
	if c.fallbackClient == nil {
		if primary != nil {
			return primary
		}
		return nil
	}
	fallback := c.fallbackClient.blockedQuota(ctx)
	if fallback == nil || (primary == nil && !sessionDowngraded(ctx)) {
		return nil
	}
	// The earliest recovery permits the scheduler to resume a fresh PR.
	if primary != nil && primary.RetryAt.Before(fallback.RetryAt) {
		return primary
	}
	return fallback
}
