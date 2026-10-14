// Package scheduler 实现定时轮询调度：time.Ticker + 随机 jitter，
// 支持外部手动触发。扫描轮次可重叠，单个审核任务由审查服务去重。
package scheduler

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// Scheduler 定时调度器。
type Scheduler struct {
	interval time.Duration
	jitter   time.Duration
	trigger  chan struct{}
	logger   *slog.Logger
	wg       sync.WaitGroup
}

// New 创建调度器。
func New(interval, jitter time.Duration, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		interval: interval,
		jitter:   jitter,
		trigger:  make(chan struct{}, 1),
		logger:   logger,
	}
}

// Trigger 手动触发一轮任务（非阻塞，已有待处理触发时忽略）。
func (s *Scheduler) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run 阻塞运行调度循环，直到 ctx 取消。启动时立即执行一轮。
func (s *Scheduler) Run(ctx context.Context, task func(context.Context)) {
	s.logger.Info("调度器启动", "interval", s.interval.String(), "jitter", s.jitter.String())

	runOnce := func(reason string) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()

			// 随机抖动，避免多实例同时请求
			if s.jitter > 0 && reason == "tick" {
				d := time.Duration(rand.Int63n(int64(s.jitter)))
				select {
				case <-ctx.Done():
					return
				case <-time.After(d):
				}
			}
			start := time.Now()
			s.logger.Info("开始执行审查轮次", "reason", reason)
			task(ctx)
			s.logger.Info("审查轮次结束", "reason", reason, "elapsed", time.Since(start).String())
		}()
	}

	runOnce("startup")

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("调度器退出")
			s.wg.Wait()
			return
		case <-ticker.C:
			runOnce("tick")
		case <-s.trigger:
			runOnce("manual")
		}
	}
}
