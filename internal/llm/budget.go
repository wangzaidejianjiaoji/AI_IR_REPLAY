package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"gvs-pr-agent/internal/config"
	"sync"
)

type budgetKey struct{}
type BudgetLimits struct {
	MaxCalls, MaxTokens int
	MaxCost             float64
	Prices              map[string]config.ModelPrice
}
type roundBudget struct {
	mu            sync.Mutex
	limits        BudgetLimits
	calls, tokens int
	cost          float64
	err           error
	cancel        context.CancelFunc
}

// WithBudget shares a budget across all model operations and retries in a round.
func WithBudget(ctx context.Context, limits BudgetLimits) (context.Context, context.CancelFunc) {
	if ctx.Value(budgetKey{}) != nil {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	return context.WithValue(ctx, budgetKey{}, &roundBudget{limits: limits, cancel: cancel}), cancel
}

func BudgetError(ctx context.Context) error {
	if b, ok := ctx.Value(budgetKey{}).(*roundBudget); ok {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.err
	}
	return nil
}

func reserveRequest(ctx context.Context, model string, messages []Message, maxOutput int) error {
	b, ok := ctx.Value(budgetKey{}).(*roundBudget)
	if !ok {
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, _ := json.Marshal(messages)
	// Byte-based upper estimate plus framing/output reserve. Never refund failed
	// requests, because a network error cannot prove the supplier did not bill.
	input := len(payload) + 256
	tokens := input + maxOutput
	cost := float64(0)
	if b.limits.MaxCost > 0 {
		p, found := b.limits.Prices[model]
		if !found {
			b.err = fmt.Errorf("轮次费用预算缺少模型 %s 价格", model)
		} else {
			cost = (float64(input)*p.InputPerMillion + float64(maxOutput)*p.OutputPerMillion) / 1000000
		}
	}
	if b.err == nil && ((b.limits.MaxCalls > 0 && b.calls+1 > b.limits.MaxCalls) || (b.limits.MaxTokens > 0 && b.tokens+tokens > b.limits.MaxTokens) || (b.limits.MaxCost > 0 && b.cost+cost > b.limits.MaxCost)) {
		b.err = fmt.Errorf("轮次模型预算耗尽：已预留调用 %d、Token %d、费用 %.6f", b.calls, b.tokens, b.cost)
	}
	if b.err != nil {
		b.cancel()
		return b.err
	}
	b.calls++
	b.tokens += tokens
	b.cost += cost
	return nil
}
