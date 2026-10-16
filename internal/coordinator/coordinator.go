// Package coordinator 实现多 Agent 协调器：
// 将审查任务按「审查包 × Agent 角色」展开为作业，按优先级分批并行调度
// （goroutine + channel + sync.WaitGroup），收集结果后依次执行
// 评论定位（locator）、评论反思与跨 Agent 去重（reflector），最终按严重度排序汇总。
package coordinator

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/diff"
	"gvs-pr-agent/internal/llm"
	"gvs-pr-agent/internal/locator"
	"gvs-pr-agent/internal/packager"
	"gvs-pr-agent/internal/reflector"
	"gvs-pr-agent/internal/textbudget"
)

// Coordinator 多 Agent 协调器。
type Coordinator struct {
	agents      []agent.Agent
	concurrency int
	loc         *locator.Locator     // 评论定位器（nil = 禁用）
	refl        *reflector.Reflector // 评论反思器（nil = 禁用）
	confirmer   *Confirmer           // 统筹确认 Agent（nil = 兼容旧流程）
	logger      *slog.Logger
}

// Summary 一次审查的汇总结果。
type Summary struct {
	// ChangeSummary 由全局理解阶段生成，说明本次 diff 的实际变更意图。
	ChangeSummary string
	// VerificationOverview 披露全局理解、规则和专业 Agent 的执行范围。
	VerificationOverview string
	// CoordinationOverview 说明统筹 Agent 对候选问题的确认、合并和驳回结果。
	CoordinationOverview      string
	CandidateCount            int
	RejectedCount             int
	ConfirmationError         string
	Issues                    []agent.Issue // 定位/反思后的问题列表（按严重度排序）
	AgentNames                []string      // 参与审查的 Agent 显示名（去重）
	Packages                  []string      // 审查包名列表
	FailedRoles               []string      // 审查失败的 Agent 角色（去重）
	TotalUsage                llm.Usage     // 累计 Token 消耗
	Dropped                   int           // 被定位校验/反思校验/去重过滤掉的问题数
	AllFailed                 bool          // 所有审查任务均失败（如 LLM 认证错误），结果不可信
	TotalAgents               int           // 已启用的 Agent 总数（含未被路径分配到的角色）
	TotalFiles                int           // 筛选后的变更文件总数（由调用方填充）
	Reviewed                  int           // 实际纳入审查的文件数（超限截断后，由调用方填充）
	SuccessfulJobs            int
	FailedJobs                int
	FullySuccessfulRoles      int
	PartiallyFailedRoles      int
	FullyFailedRoles          int
	UnconfirmedCount          int
	TruncatedPackages         []string
	AnalysisTotalBytes        int
	AnalysisReviewedBytes     int
	ConfirmationTotalBytes    int
	ConfirmationReviewedBytes int
}

// Options 单次审查运行的选项。
type Options struct {
	MaxDiffChars int  // 单包 diff 截断长度
	AssignAgents bool // 是否按包内路径动态分配 Agent（false = 全部 Agent 参与所有包）
}

// New 创建协调器。loc / refl 可为 nil 表示禁用对应确定性工程环节。
func New(agents []agent.Agent, concurrency int, loc *locator.Locator, refl *reflector.Reflector, logger *slog.Logger, confirmer ...*Confirmer) *Coordinator {
	if concurrency <= 0 {
		concurrency = 4
	}
	c := &Coordinator{agents: agents, concurrency: concurrency, loc: loc, refl: refl, logger: logger}
	if len(confirmer) > 0 {
		c.confirmer = confirmer[0]
	}
	return c
}

// job 一个「审查包 × Agent」作业。
type job struct {
	agentIdx int
	pkg      *packager.Package
}

// Run 将各审查包分发到 Agent 并行审查，随后定位、反思、排序汇总。
// base 为任务模板（Diff/Rules 字段由协调器按包填充）；
// rulesFor 按 (角色, 包内文件) 返回该 Agent 适用的规则集合。
func (c *Coordinator) Run(ctx context.Context, base agent.Task, pkgs []packager.Package,
	rulesFor func(role string, files []*diff.FileDiff) []string, files []*diff.FileDiff, opt Options) Summary {

	// 展开作业：按优先级分组（priority=1 的 Agent 先并行执行，其余批次随后）
	byPriority := map[int][]job{}
	for i := range pkgs {
		pkg := &pkgs[i]
		var roles map[string]bool
		if opt.AssignAgents {
			roles = packager.AgentsFor(*pkg)
		}
		for ai, a := range c.agents {
			if roles != nil && !roles[a.Role()] {
				continue
			}
			byPriority[a.Priority()] = append(byPriority[a.Priority()], job{agentIdx: ai, pkg: pkg})
		}
	}
	priorities := make([]int, 0, len(byPriority))
	for p := range byPriority {
		priorities = append(priorities, p)
	}
	sort.Ints(priorities)

	var (
		mu      sync.Mutex
		results []agent.Result
	)
	sem := make(chan struct{}, c.concurrency)

	for _, p := range priorities {
		var wg sync.WaitGroup
		for _, j := range byPriority[p] {
			wg.Add(1)
			go func(j job) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					mu.Lock()
					results = append(results, agent.Result{Role: c.agents[j.agentIdx].Role(), Name: c.agents[j.agentIdx].DisplayName(), Err: ctx.Err()})
					mu.Unlock()
					return
				}
				defer func() { <-sem }()

				a := c.agents[j.agentIdx]
				t := base
				t.Package = j.pkg.Name
				t.Diff = j.pkg.DiffText(opt.MaxDiffChars)
				t.Rules = rulesFor(a.Role(), j.pkg.Files)

				c.logger.Info("agent 开始审查", "agent", a.Role(), "package", j.pkg.Name, "model", t.Model)
				r := a.Review(ctx, t)
				c.logger.Info("agent 审查完成", "agent", a.Role(), "package", j.pkg.Name,
					"issues", len(r.Issues), "tokens", r.Usage.TotalTokens, "error", errString(r.Err))

				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}(j)
		}
		wg.Wait()
	}

	sum := c.aggregate(results, pkgs, files)
	for _, pkg := range pkgs {
		if len(pkg.DiffText(0)) > len(textbudget.Limit(pkg.DiffText(0), opt.MaxDiffChars)) {
			sum.TruncatedPackages = append(sum.TruncatedPackages, pkg.Name)
		}
	}
	if c.confirmer != nil && !sum.AllFailed && sum.FailedJobs == 0 && len(sum.TruncatedPackages) == 0 {
		var fullDiff strings.Builder
		for _, file := range files {
			fullDiff.WriteString(file.Patch)
			fullDiff.WriteByte('\n')
		}
		sum.CandidateCount = len(sum.Issues)
		confirmed, usage, err := c.confirmer.Confirm(ctx, ConfirmationInput{
			ChangeContext: base.ChangeContext,
			Diff:          fullDiff.String(),
			Candidates:    sum.Issues,
			Model:         base.Model,
		})
		sum.TotalUsage.PromptTokens += usage.PromptTokens
		sum.TotalUsage.CompletionTokens += usage.CompletionTokens
		sum.TotalUsage.TotalTokens += usage.TotalTokens
		if err != nil {
			sum.ConfirmationError = err.Error()
		} else {
			sum.Issues = confirmed.Issues
			sum.RejectedCount = confirmed.RejectedCount
			sum.UnconfirmedCount = confirmed.UnconfirmedCount
			sum.ConfirmationTotalBytes = confirmed.DiffTotalBytes
			sum.ConfirmationReviewedBytes = confirmed.DiffReviewedBytes
			sum.CoordinationOverview = confirmed.Overview
			if c.refl != nil {
				var dropped int
				sum.Issues, dropped = c.refl.Reflect(sum.Issues, files)
				sum.Dropped += dropped
			}
		}
	}
	return sum
}

// aggregate 汇总所有作业结果：定位校验 -> 反思校验与去重 -> 严重度排序。
func (c *Coordinator) aggregate(results []agent.Result, pkgs []packager.Package, files []*diff.FileDiff) Summary {
	sum := Summary{}
	for i := range pkgs {
		sum.Packages = append(sum.Packages, pkgs[i].Name)
	}

	seenAgent := map[string]bool{}
	seenFailed := map[string]bool{}
	succeeded := map[string]bool{}
	failed := map[string]bool{}
	var all []agent.Issue
	failedCount := 0
	for _, r := range results {
		sum.TotalUsage.PromptTokens += r.Usage.PromptTokens
		sum.TotalUsage.CompletionTokens += r.Usage.CompletionTokens
		sum.TotalUsage.TotalTokens += r.Usage.TotalTokens
		if r.Err != nil {
			failedCount++
			sum.FailedJobs++
			failed[r.Role] = true
			if !seenFailed[r.Role] {
				seenFailed[r.Role] = true
				sum.FailedRoles = append(sum.FailedRoles, r.Role)
			}
			continue
		}
		sum.SuccessfulJobs++
		succeeded[r.Role] = true
		if !seenAgent[r.Name] {
			seenAgent[r.Name] = true
			sum.AgentNames = append(sum.AgentNames, r.Name)
		}
		all = append(all, r.Issues...)
	}
	// 所有任务均失败（如 API Key 无效）：结果不可信，调用方应放弃发布
	sum.AllFailed = len(results) == 0 || failedCount == len(results)
	for role := range succeeded {
		if failed[role] {
			sum.PartiallyFailedRoles++
		} else {
			sum.FullySuccessfulRoles++
		}
	}
	for role := range failed {
		if !succeeded[role] {
			sum.FullyFailedRoles++
		}
	}
	sum.TotalAgents = len(c.agents) // 已启用总数（供报告展示“参与 N/M”）

	sort.Strings(sum.AgentNames)
	sort.Strings(sum.FailedRoles)
	sortIssues(all)

	// 【确定性工程】评论定位：文件与行号校验（过滤幻觉文件、修正偏移行号）
	if c.loc != nil {
		var dropped int
		all, dropped = c.loc.Locate(all, files)
		sum.Dropped += dropped
	}

	// 【确定性工程】评论反思：准确性校验 + 跨 Agent 去重合并
	if c.refl != nil {
		var dropped int
		all, dropped = c.refl.Reflect(all, files)
		sum.Dropped += dropped
	}

	sortIssues(all)
	sum.Issues = all
	return sum
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
