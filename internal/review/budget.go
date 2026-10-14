package review

import (
	"context"
	"gvs-pr-agent/internal/llm"
	"time"
)

func (s *Service) roundContext(ctx context.Context) (context.Context, context.CancelFunc) {
	calls, tokens := s.cfg.Review.MaxLLMCalls, s.cfg.Review.MaxRoundTokens
	if calls == 0 {
		calls = 100
	}
	if tokens == 0 {
		tokens = 1000000
	}
	duration := 20 * time.Minute
	if s.cfg.Review.RoundTimeout != "" {
		duration, _ = time.ParseDuration(s.cfg.Review.RoundTimeout)
	}
	ctx, deadlineCancel := context.WithTimeout(ctx, duration)
	ctx, budgetCancel := llm.WithBudget(ctx, llm.BudgetLimits{MaxCalls: calls, MaxTokens: tokens, MaxCost: s.cfg.Review.MaxRoundCost, Prices: s.cfg.LLM.Prices})
	return ctx, func() { budgetCancel(); deadlineCancel() }
}
