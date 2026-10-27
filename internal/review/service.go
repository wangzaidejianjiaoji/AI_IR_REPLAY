// Package review 实现审查主流程：
// PR 发现 -> Commit SHA 去重 -> diff 获取与解析 -> 文件筛选 -> 智能文件打包
// -> 规则匹配（四层链，热更新）-> 多 Agent 并行审查 -> 评论定位 -> 评论反思/去重
// -> 统一评论生成（demo 模式下评论发布被禁用，审查报告输出到日志）。
//
// 除 PR 审查外，还支持 Codeup 分支推送审查：轮询分支 HEAD 变化，
// 对新提交做增量 diff 审查（基线持久化到本地），结果回写为 commit 评论。
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/analyzer"
	"gvs-pr-agent/internal/codeup"
	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/coordinator"
	"gvs-pr-agent/internal/diff"
	"gvs-pr-agent/internal/filter"
	"gvs-pr-agent/internal/github"
	"gvs-pr-agent/internal/llm"
	"gvs-pr-agent/internal/locator"
	"gvs-pr-agent/internal/notifier"
	"gvs-pr-agent/internal/packager"
	"gvs-pr-agent/internal/progress"
	"gvs-pr-agent/internal/reflector"
	"gvs-pr-agent/internal/rules"
	"gvs-pr-agent/internal/state"
	"gvs-pr-agent/internal/textbudget"
	"gvs-pr-agent/internal/understanding"
)

// conflictTracker 跟踪已知冲突的 PR/MR，避免重复打印日志。
// 进程重启后清空，首次遇到冲突时会打印日志，后续轮询静默跳过。
type conflictTracker struct {
	mu        sync.Mutex
	conflicts map[string]struct{} // key: "github:{repo}:{pr}" 或 "codeup:{repo}:{mr}"
}

// mark 标记某个 PR/MR 为已知冲突。返回 true 表示是首次记录（应打印日志）。
func (ct *conflictTracker) mark(key string) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.conflicts == nil {
		ct.conflicts = make(map[string]struct{})
	}
	if _, ok := ct.conflicts[key]; ok {
		return false // 已记录过，静默
	}
	ct.conflicts[key] = struct{}{}
	return true // 首次记录，应打印日志
}

// clear 清除指定 key 的记录（冲突解决后调用）。
func (ct *conflictTracker) clear(key string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.conflicts, key)
}

// Service 审查服务。
type Service struct {
	cfg        *config.Config
	gh         *github.Client
	cu         *codeup.Client // Codeup 客户端（分支推送审查，未启用时为 nil）
	branchSt   *state.Store   // 分支审查基线存储（未启用时为 nil）
	llm        *llm.Client
	coord      *coordinator.Coordinator
	analyzer   *analyzer.Analyzer
	knowledge  *understanding.Manager
	rulesStore *rules.Store
	logger     *slog.Logger
	tz         *time.Location
	conflictTr *conflictTracker // 冲突 PR/MR 日志去重跟踪器
	tasks      taskTracker
}

// NewService 组装审查服务的全部依赖。
// cliRulePath 为 CLI -rule 参数指定的规则文档（四层优先级链的最高层，可为空）。
func NewService(cfg *config.Config, cliRulePath string, logger *slog.Logger) (*Service, error) {
	if cfg.Review.RuntimeDir == "" {
		cfg.Review.RuntimeDir = "./data/review-runtime"
	}
	if err := cfg.ValidateRuntime(); err != nil {
		return nil, err
	}
	// 【四层规则优先级链】CLI -rule > 项目配置 > 全局配置 > 内嵌默认
	rulesStore, err := rules.NewStore(cliRulePath, cfg.Rules.ProjectPath, cfg.Rules.GlobalPath, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("规则文档已加载", "source", rulesStore.Source(),
		"version", rulesStore.Current().Version)

	llmClient := llm.New(cfg.LLM, logger)
	agents := agent.BuildEnabled(cfg.Agents, llmClient, logger)
	if len(agents) == 0 {
		return nil, fmt.Errorf("没有启用任何 Agent，请检查配置 agents 段")
	}
	logger.Info("已启用 Agent", "count", len(agents), "roles", roleNames(agents))

	// 【确定性工程】评论定位器与评论反思器（按配置开关装配）
	var loc *locator.Locator
	if cfg.Review.CommentLocation.Enabled {
		loc = locator.New(cfg.Review.CommentLocation.VerifyLineNumber, logger)
	}
	var refl *reflector.Reflector
	if cfg.Review.CommentReflection.Enabled {
		refl = reflector.New(cfg.Review.CommentReflection.CheckHallucination, logger)
	}

	// 显式使用上海时区（跨平台部署时不依赖系统时区）
	tz, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		tz = time.Local
	}

	// Codeup 审查（可选）：客户端 + 分支基线存储
	var cu *codeup.Client
	var branchSt *state.Store
	if cfg.Codeup.Enabled {
		cu = codeup.New(cfg.Codeup.Domain, cfg.Codeup.OrganizationID, cfg.Codeup.Token, cfg.Codeup.PerPage)
		branchSt, err = state.Open(cfg.Codeup.StatePath)
		if err != nil {
			return nil, fmt.Errorf("加载分支审查基线: %w", err)
		}
		if cfg.Codeup.BranchReview {
			logger.Info("分支推送审查已启用", "repos", len(cfg.Codeup.Repos),
				"state_path", cfg.Codeup.StatePath)
		} else {
			logger.Info("分支推送审查未启用（branch_review=false），仅审查不在开发中的 MR")
		}
	}

	var changeAnalyzer *analyzer.Analyzer
	var knowledgeManager *understanding.Manager
	var confirmer *coordinator.Confirmer
	if cfg.Understanding.Enabled {
		var seed string
		if cfg.Understanding.SeedPath != "" {
			raw, readErr := os.ReadFile(cfg.Understanding.SeedPath)
			if readErr != nil {
				if cfg.Understanding.Strict {
					return nil, fmt.Errorf("读取全局理解初始化材料: %w", readErr)
				}
				logger.Warn("全局理解初始化材料不可用，将使用现有本地理解文档", "error", readErr.Error())
			} else {
				seed = string(raw)
			}
		}
		changeAnalyzer = analyzer.New(llmClient)
		knowledgeManager = understanding.New(cfg.Understanding, cu, llmClient, seed, logger)
		confirmer = coordinator.NewConfirmer(llmClient)
	}

	return &Service{
		cfg:        cfg,
		gh:         github.New(cfg.GitHub.APIBase, cfg.GitHub.Token, cfg.GitHub.PerPage),
		cu:         cu,
		branchSt:   branchSt,
		llm:        llmClient,
		coord:      coordinator.New(agents, cfg.Review.MaxAgentConcurrency, loc, refl, logger, confirmer),
		analyzer:   changeAnalyzer,
		knowledge:  knowledgeManager,
		rulesStore: rulesStore,
		logger:     logger,
		tz:         tz,
		conflictTr: &conflictTracker{},
		tasks:      taskTracker{logger: logger},
	}, nil
}

// StartRuleHotReload 启动规则文档热更新轮询（rules.hot_reload 开启时）。
func (s *Service) StartRuleHotReload(stop <-chan struct{}) {
	if s.cfg.Rules.HotReload {
		s.rulesStore.StartHotReload(s.cfg.Rules.ReloadIntervalDuration(), stop)
	}
}

// RunOnce 执行一轮审查：遍历所有配置仓库的 Open PR，再扫描 Codeup 分支推送。
func (s *Service) RunOnce(ctx context.Context) {
	// 【审查时间窗口】非工作时段跳过本轮（与 GVS 日夜模式对应）
	if !s.cfg.Review.InActiveHours(time.Now()) {
		s.logger.Info("当前不在审查时间窗口内，跳过本轮",
			"start", s.cfg.Review.ActiveHours.Start, "end", s.cfg.Review.ActiveHours.End)
		return
	}
	var candidates []reviewCandidate
	ctx = context.WithValue(ctx, collectionKey{}, &candidates)
	for _, repo := range s.cfg.GitHub.Repos {
		if err := s.reviewRepo(ctx, repo); err != nil {
			s.logger.Error("仓库审查失败", "repo", repo.Owner+"/"+repo.Repo, "error", err.Error())
		}
	}
	// 【分支推送审查】扫描 Codeup 仓库分支 HEAD 变化，增量审查新提交；
	// 默认关闭：分支推送属开发中代码，审查对象应仅为不在开发中的 MR
	if s.cu != nil {
		for _, repo := range s.cfg.Codeup.Repos {
			// 【MR 审查】扫描 Codeup 开启状态的合并请求（新版本未审时触发）
			if s.cfg.Codeup.MRReview {
				if err := s.reviewCodeupMRs(ctx, repo); err != nil {
					s.logger.Error("MR 审查失败", "repo", repo.DisplayName(), "error", err.Error())
				}
			}
			if s.cfg.Codeup.BranchReview {
				if err := s.reviewCodeupRepo(ctx, repo); err != nil {
					s.logger.Error("分支推送审查失败", "repo", repo.DisplayName(), "error", err.Error())
				}
			}
		}
	}
	ctx = context.WithValue(ctx, collectionKey{}, false)
	if err := s.executeCollected(ctx, candidates); err != nil {
		s.logger.Warn("本轮审核未完成", "error", err)
	}
}

// reviewRepo 审查单个仓库的所有符合条件的 Open PR。
func (s *Service) reviewRepo(ctx context.Context, repo config.RepoConfig) error {
	full := repo.Owner + "/" + repo.Repo
	prs, err := s.gh.ListOpenPRs(ctx, repo.Owner, repo.Repo)
	if err != nil {
		return err
	}
	s.logger.Info("获取 Open PR 列表", "repo", full, "count", len(prs))

	for _, pr := range prs {
		key := fmt.Sprintf("github:%s:%d", full, pr.Number)
		// 【WIP 检测】开发中的 PR 暂不审查，移除 WIP 前缀后自动进入审查
		if pr.IsWIP() {
			s.logger.Debug("跳过 PR：开发中（WIP）", "pr", pr.Number, "title", pr.Title)
			continue
		}
		// 【合并冲突检测】存在冲突的 PR 无法生成 diff，跳过以节省 API 与 LLM 配额
		if pr.HasConflict() {
			if s.conflictTr.mark(key) {
				s.logger.Info("跳过 PR：存在合并冲突，等待开发者解决后自动重审",
					"pr", pr.Number, "mergeable_state", pr.MergeableState)
			}
			continue
		}
		// PR 无冲突时，清除冲突跟踪记录（防止冲突解决后重新冲突时漏打日志）
		s.conflictTr.clear(key)
		// 目标分支过滤
		if !filter.MatchBranch(pr.Base.Ref, repo.BranchFilter.Include, repo.BranchFilter.Exclude) {
			s.logger.Debug("跳过 PR：目标分支不匹配", "pr", pr.Number, "base", pr.Base.Ref)
			continue
		}
		if err := s.reviewPR(ctx, repo, pr); err != nil {
			s.logger.Error("PR 审查失败", "repo", full, "pr", pr.Number, "error", err.Error())
		}
	}
	return nil
}

// reviewPR 审查单个 PR。
func (s *Service) reviewPR(ctx context.Context, repo config.RepoConfig, pr *github.PullRequest) error {
	key := fmt.Sprintf("github:%s/%s:%d", repo.Owner, repo.Repo, pr.Number)
	return s.runTask(ctx, key, pr.Head.SHA+":"+pr.Base.SHA+":"+fingerprint([]string{pr.Title, pr.Body}), func(ctx context.Context) error {
		return s.reviewPRSnapshot(ctx, repo, pr)
	})
}

func (s *Service) reviewPRSnapshot(ctx context.Context, repo config.RepoConfig, pr *github.PullRequest) error {
	full := repo.Owner + "/" + repo.Repo
	log := s.logger.With("repo", full, "pr", pr.Number, "sha", short(pr.Head.SHA))

	// 【第一层去重】检查是否已有该 Commit SHA 的审查存证
	comments, err := s.gh.ListComments(ctx, repo.Owner, repo.Repo, pr.Number)
	if err != nil {
		return err
	}
	inputSignature := fingerprint([]string{s.signature(s.reviewRules(ctx)), pr.Title, pr.Body})
	marker := notifier.Marker(pr.Head.SHA + ":" + pr.Base.SHA + ":" + inputSignature)
	for _, c := range comments {
		if strings.Contains(c.Body, marker) {
			log.Debug("跳过 PR：该 Commit 已审查")
			return nil
		}
	}

	// 获取并解析 diff
	if pr.Head.SHA == "" || pr.Base.SHA == "" {
		return fmt.Errorf("PR 缺少固定的源/目标 SHA")
	}
	rawDiff, err := s.gh.GetCompareDiff(ctx, repo.Owner, repo.Repo, pr.Base.SHA, pr.Head.SHA)
	if err != nil {
		// 兜底：区分"合并冲突"与其他错误，避免冲突 PR 反复报错
		if strings.Contains(err.Error(), "合并冲突") {
			key := fmt.Sprintf("github:%s:%d", full, pr.Number)
			if s.conflictTr.mark(key) {
				log.Info("跳过 PR：存在合并冲突，等待开发者解决后自动重审", "error", err.Error())
			}
			// A conflict is not a completed review; allow the same SHA to retry.
			return err
		}
		return err
	}

	if err := s.checkPRVersion(ctx, repo, pr); err != nil {
		return err
	}
	ctx = s.checkpointContext(ctx, fmt.Sprintf("github:%s:%d:%s:%s:%s", full, pr.Number, pr.Head.SHA, pr.Base.SHA, fingerprint(rawDiff)))
	summary, err := s.reviewDiff(ctx, rawDiff, full, pr.Title, pr.Body, log)
	if err != nil || summary == nil {
		return err
	}
	if err := s.checkPRVersion(ctx, repo, pr); err != nil {
		return err
	}

	// 生成统一评论
	body := notifier.BuildComment(*summary, notifier.Options{
		MentionAuthor:    s.cfg.Notification.MentionAuthor,
		MentionReviewers: s.cfg.Notification.MentionReviewers,
		MaxIssuesDisplay: s.cfg.Notification.MaxIssuesDisplay,
		Author:           pr.User.Login,
		Reviewers:        pr.Reviewers(),
		CommitSHA:        pr.Head.SHA + ":" + pr.Base.SHA + ":" + inputSignature,
		Timezone:         s.tz,
	})

	// 【评论发布开关】demo 模式禁用发布：审查报告只输出到日志
	if !s.cfg.Notification.PRComment || s.cfg.Review.DryRun {
		log.Info("评论发布已禁用（模拟审查），审查报告如下",
			"issues", len(summary.Issues), "report", body)
		return nil
	}
	if err := s.publishOnce(ctx, "github:"+full+":"+fmt.Sprint(pr.Number)+":"+marker, body, func() error { return s.gh.PostComment(ctx, repo.Owner, repo.Repo, pr.Number, body) }); err != nil {
		return err
	}
	log.Info("评论已发布")
	return nil
}

// reviewDiff 审查流程公共段：解析 -> 筛选 -> 打包 -> 多 Agent 审查 -> 定位/反思汇总。
// 筛选后无文件时返回 (nil, nil)。
func (s *Service) reviewDiff(ctx context.Context, rawDiff, repoFullName, title, body string,
	log *slog.Logger) (*coordinator.Summary, error) {
	progress.Stage(ctx, "preparing", repoFullName, title)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allFiles := diff.Parse(rawDiff)

	// 【确定性工程】文件筛选
	files := filter.Files(allFiles, s.cfg.Review.IncludePaths, s.cfg.Review.ExcludePaths)
	if len(files) == 0 {
		log.Info("跳过：筛选后无待审查文件", "total_files", len(allFiles))
		return nil, nil
	}
	if attempt, ok := ctx.Value(pipelineKey{}).(*pipelineAttempt); ok {
		attempt.started = true
	}
	if err := s.llm.Availability(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		log.Info("模型处于冷却期，先复用已有检查点；缺失结果时等待可用模型", "error", err)
	}
	totalFiltered := len(files)
	// 大 MR 不再按文件数拦截：规模由分包、轮次预算（max_llm_calls/max_round_tokens）与时长约束。

	// 【确定性工程】智能文件打包：关联文件归并为审查包，上下文隔离
	var pkgs []packager.Package
	if s.cfg.Review.FilePackaging.Enabled {
		var err error
		pkgs, err = packager.PackBounded(files, s.cfg.Review.FilePackaging.MaxPackageSize, s.cfg.Review.MaxDiffChars)
		if err != nil {
			return nil, err
		}
	} else {
		pkgs = []packager.Package{{Name: "全部变更", Files: files}}
	}
	for _, pkg := range pkgs {
		if s.cfg.Review.MaxDiffChars > 0 && len(pkg.DiffText(0)) > s.cfg.Review.MaxDiffChars {
			return nil, fmt.Errorf("审查覆盖不完整：包 %s 的 diff 超过 max_diff_chars=%d；请启用文件分包或提高预算，拒绝截断审核", pkg.Name, s.cfg.Review.MaxDiffChars)
		}
	}
	var pkgNames []string
	for i := range pkgs {
		pkgNames = append(pkgNames, fmt.Sprintf("%s(%d文件)", pkgs[i].Name, len(pkgs[i].Files)))
	}

	// 【模型分级】按变更总行数选择模型
	changed := diff.TotalChangedLines(files)
	model := s.llm.SelectModel(changed)
	log.Info("开始审查", "title", title, "files", len(files), "packages", strings.Join(pkgNames, "、"),
		"changed_lines", changed, "model", model)

	var (
		changeContext   analyzer.ChangeContext
		analysisUsage   llm.Usage
		knowledgeUsage  llm.Usage
		knowledgeSHA    string
		knowledgeStatus = "未启用"
	)
	if s.analyzer != nil {
		var globalDocument string
		if s.knowledge != nil {
			progress.Stage(ctx, "understanding", "", "")
			snapshot, usage, prepareErr := s.prepareKnowledge(ctx, model)
			knowledgeUsage = usage
			if prepareErr != nil {
				var persistence snapshotPersistenceError
				if errors.As(prepareErr, &persistence) {
					return nil, prepareErr
				}
				knowledgeStatus = "降级：" + prepareErr.Error()
				if s.cfg.Understanding.Strict {
					return nil, prepareErr
				}
				globalDocument, knowledgeSHA = snapshot.Document, snapshot.SourceSHA
				log.Warn("本地全局理解更新失败，使用已标注版本的旧快照（若有）", "source_sha", knowledgeSHA, "error", prepareErr.Error())
			} else {
				globalDocument = snapshot.Document
				knowledgeSHA = snapshot.SourceSHA
				if snapshot.Updated {
					knowledgeStatus = "已同步"
				} else {
					knowledgeStatus = "已复用最新版本"
				}
			}
		}
		var analyzeErr error
		progress.Stage(ctx, "analysis", "", "")
		analysisInput := analyzer.Input{
			Repository: repoFullName, Title: title, Body: body,
			Diff: analysisDiff(files, 0), GlobalUnderstanding: globalDocument, Model: model,
		}
		if len(analysisInput.Diff) > s.cfg.Review.MaxDiffChars && s.cfg.Review.MaxDiffChars > 0 {
			var batches []string
			for _, pkg := range pkgs {
				batches = append(batches, pkg.DiffText(0))
			}
			changeContext, analysisUsage, analyzeErr = s.analyzer.AnalyzeBatches(ctx, analysisInput, batches, s.cfg.Review.MaxDiffChars)
		} else {
			changeContext, analysisUsage, analyzeErr = s.analyzer.Analyze(ctx, analysisInput)
		}
		if analyzeErr != nil {
			return nil, analyzeErr
		}
	}
	changeJSON, _ := json.Marshal(changeContext)
	base := agent.Task{
		RepoFullName:  repoFullName,
		PRTitle:       title,
		PRBody:        body,
		ChangeContext: string(changeJSON),
		Model:         model,
	}

	// 多 Agent 并行审查（按包 × 角色分发）+ 定位 + 反思去重
	doc := s.reviewRules(ctx)
	progress.Stage(ctx, "reviewing/confirming", "", "")
	summary := s.coord.Run(ctx, base, pkgs, func(role string, pkgFiles []*diff.FileDiff) []string {
		return doc.ForAgent(role, pkgFiles)
	}, files, coordinator.Options{
		MaxDiffChars: s.cfg.Review.MaxDiffChars,
		AssignAgents: s.cfg.Review.FilePackaging.Enabled,
	})
	summary.TotalFiles, summary.Reviewed = totalFiltered, len(files)
	summary.AnalysisTotalBytes = len(analysisDiff(files, 0))
	if s.analyzer != nil {
		summary.AnalysisReviewedBytes = summary.AnalysisTotalBytes
	}
	summary.ChangeSummary = changeContext.Summary
	if changeContext.Intent != "" {
		summary.ChangeSummary += "\n\n**变更意图：** " + changeContext.Intent
	}
	summary.VerificationOverview = fmt.Sprintf("全局理解文档：%s", knowledgeStatus)
	if knowledgeSHA != "" {
		// 知识库跟踪目标仓库默认分支（main）；knowledgeRepo 取仓库名最后一段，
		// 避免多仓库共用时把 gvs_infer 误标为 gvs（本地模式显示 local）。
		knowledgeRepo := repoFullName
		if knowledgeRepo == "local/diff" {
			knowledgeRepo = "local"
		} else if i := strings.LastIndex(knowledgeRepo, "/"); i >= 0 {
			knowledgeRepo = knowledgeRepo[i+1:]
		}
		summary.VerificationOverview += fmt.Sprintf("（%s/main `%s`）", knowledgeRepo, short(knowledgeSHA))
	}
	summary.VerificationOverview += fmt.Sprintf("；纳入 %d/%d 个文件（不等于全文覆盖）；作业成功 %d/失败 %d；角色全部成功 %d/部分失败 %d/全部失败 %d；定位/反思过滤 %d 个候选。",
		summary.Reviewed, summary.TotalFiles, summary.SuccessfulJobs, summary.FailedJobs, summary.FullySuccessfulRoles, summary.PartiallyFailedRoles, summary.FullyFailedRoles, summary.Dropped)
	summary.TotalUsage.PromptTokens += knowledgeUsage.PromptTokens + analysisUsage.PromptTokens
	summary.TotalUsage.CompletionTokens += knowledgeUsage.CompletionTokens + analysisUsage.CompletionTokens
	summary.TotalUsage.TotalTokens += knowledgeUsage.TotalTokens + analysisUsage.TotalTokens

	log.Info("审查完成", "issues", len(summary.Issues), "dropped", summary.Dropped,
		"failed_agents", summary.FailedRoles, "total_tokens", summary.TotalUsage.TotalTokens)

	// 【失败拦截】所有审查任务均失败（如 LLM 认证/网络故障）：
	// 结果不可信，返回错误让调用方不发评论、不推进去重基线，下轮重试
	if summary.AllFailed {
		return nil, fmt.Errorf("全部 Agent 审查失败（%s），放弃本次结果",
			strings.Join(summary.FailedRoles, "、"))
	}
	if summary.FailedJobs > 0 || len(summary.TruncatedPackages) > 0 || summary.UnconfirmedCount > 0 {
		return nil, fmt.Errorf("审查覆盖不完整：失败作业 %d、截断包 %d、未确认候选 %d；不发布评论或推进成功基线", summary.FailedJobs, len(summary.TruncatedPackages), summary.UnconfirmedCount)
	}
	if summary.ConfirmationError != "" {
		return nil, fmt.Errorf("统筹确认失败，放弃发布未经确认的问题: %s", summary.ConfirmationError)
	}
	if err := llm.BudgetError(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &summary, nil
}

func analysisDiff(files []*diff.FileDiff, maxChars int) string {
	var sb strings.Builder
	for _, file := range files {
		sb.WriteString(file.Patch)
		if !strings.HasSuffix(file.Patch, "\n") {
			sb.WriteByte('\n')
		}
	}
	value := sb.String()
	if maxChars > 0 && len(value) > maxChars {
		return textbudget.Limit(value, maxChars) + "\n...[全局理解 diff 达到预算，已截断]"
	}
	return value
}

// ReviewLocalDiff 本地 diff 审查（demo 调试入口，不依赖 GitHub）。
func (s *Service) ReviewLocalDiff(ctx context.Context, rawDiff, title string) (string, error) {
	var report string
	err := s.runTask(ctx, "local:"+fingerprint(title), "", func(ctx context.Context) error {
		ctx = s.checkpointContext(ctx, "local:"+fingerprint([]string{title, rawDiff}))
		summary, err := s.reviewDiff(ctx, rawDiff, "local/diff", title, "", s.logger)
		if err != nil {
			return err
		}
		if summary == nil {
			return fmt.Errorf("筛选后无待审查文件")
		}
		report = notifier.BuildComment(*summary, notifier.Options{MaxIssuesDisplay: s.cfg.Notification.MaxIssuesDisplay, CommitSHA: "local", Timezone: s.tz})
		return nil
	})
	return report, err
}

// reviewCodeupRepo 审查单个 Codeup 仓库的分支推送：
// 拉取分支列表 -> 分支名过滤 -> 对比本地基线发现新提交 -> 增量审查。
func (s *Service) reviewCodeupRepo(ctx context.Context, repo config.CodeupRepoConfig) error {
	name := repo.DisplayName()
	branches, err := s.cu.ListBranches(ctx, repo.RepositoryID)
	if err != nil {
		return err
	}
	s.logger.Info("获取分支列表", "repo", name, "count", len(branches))

	baseBranch := repo.BaseBranch
	if baseBranch == "" {
		baseBranch = "master"
	}

	for _, br := range branches {
		// 基准分支自身不做推送审查（其变更由 MR/PR 审查覆盖）
		if br.Name == baseBranch || br.DefaultBranch {
			continue
		}
		// 源分支名过滤
		if !filter.MatchBranch(br.Name, repo.BranchFilter.Include, repo.BranchFilter.Exclude) {
			s.logger.Debug("跳过分支：分支名不匹配", "repo", name, "branch", br.Name)
			continue
		}
		if err := s.reviewBranchPush(ctx, repo, br, baseBranch); err != nil {
			s.logger.Error("分支审查失败", "repo", name, "branch", br.Name, "error", err.Error())
		}
	}
	return nil
}

// reviewBranchPush 审查单个分支的新提交（提交分支即审查）：
// 基线与 HEAD 一致时跳过；首次发现默认仅记录基线（review_first_push 开启时
// 对比基准分支全量审查）；其余情况对 基线..HEAD 做增量审查。
// 审查成功后才推进基线，失败时下轮重试。
func (s *Service) reviewBranchPush(ctx context.Context, repo config.CodeupRepoConfig,
	br *codeup.Branch, baseBranch string) error {
	key := "branch:" + repo.RepositoryID + ":" + br.Name
	return s.runTask(ctx, key, br.Commit.ID, func(ctx context.Context) error {
		return s.reviewBranchSnapshot(ctx, repo, br, baseBranch)
	})
}

func (s *Service) reviewBranchSnapshot(ctx context.Context, repo config.CodeupRepoConfig,
	br *codeup.Branch, baseBranch string) error {

	name := repo.DisplayName()
	head := br.Commit.ID
	key := repo.RepositoryID + ":" + br.Name
	log := s.logger.With("repo", name, "branch", br.Name, "sha", short(head))

	// 【去重】基线已是当前 HEAD：无新提交
	last := s.branchSt.Get(key)
	if last == head {
		return nil
	}

	from := last
	if last == "" {
		if !s.cfg.Codeup.ReviewFirstPush {
			// 首次发现：仅记录基线，避免首轮全量审查存量分支
			log.Info("首次发现分支，记录审查基线")
			return s.branchSt.Set(key, head)
		}
		from = baseBranch // 首次全量：对比基准分支（Merge-Base）
	}

	rawDiff, commits, err := s.cu.GetCompareDiff(ctx, repo.RepositoryID, from, head)
	if err != nil {
		return err
	}

	title := fmt.Sprintf("分支 %s 新推送（%d 个提交）: %s", br.Name, len(commits), br.Commit.Title)
	ctx = s.checkpointContext(ctx, fmt.Sprintf("branch:%s:%s:%s:%s", repo.RepositoryID, br.Name, from, head))
	summary, err := s.reviewDiff(ctx, rawDiff, name, title, "", log)
	if err != nil {
		return err
	}
	if summary == nil {
		// 筛选后无待审查文件：直接推进基线，避免下轮重复扫描
		return s.branchSt.Set(key, head)
	}

	// 生成统一评论（提交人取分支最新 commit 作者）
	body := notifier.BuildComment(*summary, notifier.Options{
		MentionAuthor:    s.cfg.Notification.MentionAuthor,
		MaxIssuesDisplay: s.cfg.Notification.MaxIssuesDisplay,
		Author:           br.Commit.AuthorName,
		CommitSHA:        head,
		Timezone:         s.tz,
	})

	// 【评论发布开关】dry_run 或关闭 commit_comment 时只输出日志
	if !s.cfg.Codeup.CommitComment || s.cfg.Review.DryRun {
		log.Info("commit 评论发布已禁用（模拟审查），审查报告如下",
			"issues", len(summary.Issues), "report", body)
		return s.branchSt.Set(key, head)
	}
	if err := s.publishOnce(ctx, "branch:"+repo.RepositoryID+":"+head+":"+s.signature(s.reviewRules(ctx)), body, func() error { return s.cu.CreateCommitComment(ctx, repo.RepositoryID, head, body) }); err != nil {
		return err
	}
	log.Info("commit 评论已发布", "issues", len(summary.Issues))
	return s.branchSt.Set(key, head)
}

type focusedMRKey struct{}

// ReviewCodeupMR reviews one explicitly selected configured MR without scanning
// branch pushes. An exact selection deliberately bypasses polling branch filters.
func (s *Service) ReviewCodeupMR(ctx context.Context, repositoryID string, localID int) error {
	if strings.TrimSpace(repositoryID) == "" || localID <= 0 {
		return fmt.Errorf("Codeup 仓库与正整数 MR ID 必须指定")
	}
	if !s.cfg.Codeup.Enabled || !s.cfg.Codeup.MRReview || s.cu == nil {
		return fmt.Errorf("Codeup MR 审查未启用")
	}
	var selected *config.CodeupRepoConfig
	for i := range s.cfg.Codeup.Repos {
		repo := &s.cfg.Codeup.Repos[i]
		if repo.RepositoryID != repositoryID {
			continue
		}
		if selected != nil {
			return fmt.Errorf("Codeup 仓库重复配置: %s", repositoryID)
		}
		selected = repo
	}
	if selected == nil {
		return fmt.Errorf("Codeup 仓库未配置: %s", repositoryID)
	}
	if !s.cfg.Review.InActiveHours(time.Now()) {
		return fmt.Errorf("当前不在审查时间窗口内")
	}
	mrs, err := s.cu.ListOpenChangeRequests(ctx, repositoryID)
	if err != nil {
		return err
	}
	for _, mr := range mrs {
		if mr == nil || mr.LocalID != localID {
			continue
		}
		if mr.State == "CLOSED" || mr.State == "MERGED" {
			return fmt.Errorf("MR !%d 已关闭或合并", localID)
		}
		if mr.SourceBranch == "" || mr.TargetBranch == "" {
			return fmt.Errorf("MR !%d 缺少源分支或目标分支", localID)
		}
		if mr.IsWIP() {
			return fmt.Errorf("MR !%d 为 WIP", localID)
		}
		if mr.State == "UNDER_DEV" {
			return fmt.Errorf("MR !%d 为开发中（UNDER_DEV），提交评审后再审查", localID)
		}
		if mr.HasConflict() {
			return fmt.Errorf("MR !%d 存在合并冲突", localID)
		}
		return s.reviewChangeRequest(context.WithValue(ctx, focusedMRKey{}, true), *selected, mr)
	}
	return fmt.Errorf("未找到开启的 MR %s!%d", repositoryID, localID)
}

// reviewCodeupMRs 审查单个 Codeup 仓库的开启状态合并请求：
// MR 列表 -> 源分支过滤 -> 合并冲突检测 -> 合并源版本去重 -> diff 审查 -> MR 全局评论。
func (s *Service) reviewCodeupMRs(ctx context.Context, repo config.CodeupRepoConfig) error {
	name := repo.DisplayName()
	mrs, err := s.cu.ListOpenChangeRequests(ctx, repo.RepositoryID)
	if err != nil {
		return err
	}
	s.logger.Info("获取开启 MR 列表", "repo", name, "count", len(mrs))

	for _, mr := range mrs {
		key := fmt.Sprintf("codeup:%s:%d", repo.RepositoryID, mr.LocalID)
		// 源分支过滤（与分支推送审查共用 branch_filter，避免波及其他 MR）
		if !filter.MatchBranch(mr.SourceBranch, repo.BranchFilter.Include, repo.BranchFilter.Exclude) {
			s.logger.Debug("跳过 MR：源分支不匹配", "repo", name, "mr", mr.LocalID, "source", mr.SourceBranch)
			continue
		}
		// 【WIP 检测】开发中的 MR 暂不审查，移除 WIP 前缀后自动进入审查
		if mr.IsWIP() {
			s.logger.Debug("跳过 MR：开发中（WIP）", "repo", name, "mr", mr.LocalID, "title", mr.Title)
			continue
		}
		// 【开发中状态】UNDER_DEV 的 MR 尚未提交评审，暂不审查；
		// 提交评审（UNDER_REVIEW/TO_BE_MERGED）后自动进入审查
		if mr.State == "UNDER_DEV" {
			s.logger.Debug("跳过 MR：开发中（UNDER_DEV）", "repo", name, "mr", mr.LocalID, "title", mr.Title)
			continue
		}
		// 【合并冲突检测】存在冲突的 MR 无法生成 diff，跳过以节省 API 与 LLM 配额
		if mr.HasConflict() {
			if s.conflictTr.mark(key) {
				s.logger.Info("跳过 MR：存在合并冲突，等待开发者解决后自动重审",
					"repo", name, "mr", mr.LocalID, "merge_status", mr.MergeStatus)
			}
			continue
		}
		// MR 无冲突时，清除冲突跟踪记录
		s.conflictTr.clear(key)
		if err := s.reviewChangeRequest(ctx, repo, mr); err != nil {
			s.logger.Error("MR 审查失败", "repo", name, "mr", mr.LocalID, "error", err.Error())
		}
	}
	return nil
}

// reviewChangeRequest deduplicates code snapshots, independent of MR wording.
// Subsequent source versions use the last successful source as their diff base.
func (s *Service) reviewChangeRequest(ctx context.Context, repo config.CodeupRepoConfig,
	mr *codeup.ChangeRequest) error {
	key := fmt.Sprintf("mr:%s:%d", repo.RepositoryID, mr.LocalID)
	version := ""
	if _, collecting := ctx.Value(collectionKey{}).(*[]reviewCandidate); collecting {
		versions, err := s.cu.ListPatchSets(ctx, repo.RepositoryID, mr.LocalID)
		if err != nil {
			return err
		}
		source := codeup.SourcePatchSet(versions)
		if source == nil {
			return nil
		}
		target, err := s.mrTargetSHA(ctx, repo.RepositoryID, mr.TargetBranch, versions)
		if err != nil {
			return err
		}
		version = source.CommitID + ":" + target
	}
	// Claim the MR before fetching patch sets. Completed versions are deduplicated
	// by the persistent baseline inside reviewChangeRequestSnapshot.
	return s.runTask(ctx, key, version, func(ctx context.Context) error {
		return s.reviewChangeRequestSnapshot(ctx, repo, mr)
	})
}

func (s *Service) reviewChangeRequestSnapshot(ctx context.Context, repo config.CodeupRepoConfig,
	mr *codeup.ChangeRequest) error {

	name := repo.DisplayName()
	log := s.logger.With("repo", name, "mr", mr.LocalID, "source", mr.SourceBranch)

	// 当前待审查的合并源版本（评论关联 + 去重基线）
	ps, err := s.cu.ListPatchSets(ctx, repo.RepositoryID, mr.LocalID)
	if err != nil {
		return err
	}
	src := codeup.SourcePatchSet(ps)
	if src == nil {
		if ctx.Value(focusedMRKey{}) == true {
			return fmt.Errorf("MR 未找到合并源版本")
		}
		log.Warn("跳过 MR：未找到合并源版本")
		return nil
	}

	// 【去重】该合并源版本已审查过（新推送会产生新版本/新 CommitID）
	if ctx.Value(focusedMRKey{}) == true && (src.CommitID == "" || src.PatchSetBizID == "") {
		return fmt.Errorf("MR 源版本缺少提交 SHA 或版本 ID")
	}
	key := "mr:" + repo.RepositoryID + ":" + fmt.Sprint(mr.LocalID)
	log = log.With("sha", short(src.CommitID))

	targetSHA, err := s.mrTargetSHA(ctx, repo.RepositoryID, mr.TargetBranch, ps)
	if err != nil {
		return err
	}
	previousSHA, previousTarget := s.branchSt.GetVersion(key)
	// Existing successful snapshots also migrate without repeating their review
	// merely because older input hashes included the title and description.
	if previousSHA == src.CommitID && previousTarget == targetSHA {
		return nil
	}
	if src.CommitID == "" || src.PatchSetBizID == "" {
		return fmt.Errorf("MR 源版本缺少提交 SHA 或版本 ID")
	}
	fromSHA := targetSHA
	incremental := previousSHA != "" && previousTarget == targetSHA
	var rawDiff string
	if incremental {
		fromSHA = previousSHA
		rawDiff, _, err = s.cu.GetVersionDiff(ctx, repo.RepositoryID, fromSHA, src.CommitID)
	} else {
		rawDiff, _, err = s.cu.GetCompareDiff(ctx, repo.RepositoryID, fromSHA, src.CommitID)
	}
	if err != nil {
		return err
	}
	inputSignature := fingerprint([]string{s.signature(s.reviewRules(ctx)), "mr-code-delta-v1", fromSHA, src.CommitID, targetSHA})
	reviewScope := fmt.Sprintf("首次/目标基线变化后的完整 MR 审核：`%s...%s`", targetSHA, src.CommitID)
	if incremental {
		reviewScope = fmt.Sprintf("增量审核：上次成功版本 `%s` → 当前版本 `%s`；本报告仅覆盖这两个代码版本之间的差异", fromSHA, src.CommitID)
	}
	log.Info("MR 审核范围", "incremental", incremental, "from_sha", fromSHA, "to_sha", src.CommitID, "target_sha", targetSHA)

	title := fmt.Sprintf("MR !%d %s（%s -> %s）", mr.LocalID, mr.Title, mr.SourceBranch, mr.TargetBranch)
	scope := fmt.Sprintf("mr-code-delta-v1:%s!%d:%s:%s:%s:%s", repo.RepositoryID, mr.LocalID, fromSHA, src.CommitID, targetSHA, fingerprint(rawDiff))
	ctx = s.checkpointContext(ctx, scope)
	reviewBody := reviewScope + "\n仅审查本次提供的代码差异；MR 标题和描述是背景材料，不代表本次新增变更。\n\n" + mr.Description
	summary, err := s.reviewDiff(ctx, rawDiff, name, title, reviewBody, log)
	if err != nil {
		return err
	}
	if summary == nil && strings.TrimSpace(rawDiff) != "" && ctx.Value(focusedMRKey{}) == true {
		return fmt.Errorf("MR 无有效的待审查文件（变更被路径规则筛除）")
	}
	// The diff and comment version must remain the captured immutable snapshot.
	fresh, err := s.cu.ListPatchSets(ctx, repo.RepositoryID, mr.LocalID)
	if err != nil {
		return err
	}
	current := codeup.SourcePatchSet(fresh)
	freshTarget, err := s.mrTargetSHA(ctx, repo.RepositoryID, mr.TargetBranch, fresh)
	if err != nil {
		return err
	}
	if current == nil || current.CommitID != src.CommitID || current.PatchSetBizID == "" || freshTarget != targetSHA {
		return fmt.Errorf("MR 版本在审查期间发生变化，放弃发布及基线推进")
	}
	open, err := s.cu.ListOpenChangeRequests(ctx, repo.RepositoryID)
	if err != nil {
		return err
	}
	stillCurrent := false
	for _, item := range open {
		if item.LocalID == mr.LocalID && item.State != "CLOSED" && item.State != "MERGED" && item.SourceBranch == mr.SourceBranch && item.TargetBranch == mr.TargetBranch && !item.IsWIP() && !item.HasConflict() {
			stillCurrent = true
			break
		}
	}
	if !stillCurrent {
		return fmt.Errorf("MR 已关闭、分支或可审核状态发生变化，放弃发布及基线推进")
	}
	if summary == nil {
		log.Info("MR 无新增可审核代码，不调用模型或发布报告", "from_sha", fromSHA, "to_sha", src.CommitID)
		return s.branchSt.SetReview(key, src.CommitID, targetSHA, inputSignature)
	}
	summary.VerificationOverview = reviewScope + "\n\n" + summary.VerificationOverview

	body := notifier.BuildComment(*summary, notifier.Options{
		MentionAuthor:    s.cfg.Notification.MentionAuthor,
		MaxIssuesDisplay: s.cfg.Notification.MaxIssuesDisplay,
		Author:           mr.Author.Name,
		CommitSHA:        src.CommitID,
		Timezone:         s.tz,
	})

	// 【评论发布开关】dry_run 或关闭 mr_comment 时只输出日志
	if !s.cfg.Codeup.MRComment || s.cfg.Review.DryRun {
		log.Info("MR 评论发布已禁用（模拟审查），审查报告如下",
			"issues", len(summary.Issues), "report", body)
		return s.branchSt.SetReview(key, src.CommitID, targetSHA, inputSignature)
	}
	publicationID := fmt.Sprintf("mr:%s:%d:%s:%s:%s", repo.RepositoryID, mr.LocalID, src.CommitID, targetSHA, inputSignature)
	if err := s.publishOnce(ctx, publicationID, body, func() error {
		return s.cu.CreateChangeRequestComment(ctx, repo.RepositoryID, mr.LocalID, current.PatchSetBizID, body)
	}); err != nil {
		return err
	}
	log.Info("MR 评论已发布", "issues", len(summary.Issues))
	return s.branchSt.SetReview(key, src.CommitID, targetSHA, inputSignature)
}

func (s *Service) mrTargetSHA(ctx context.Context, repository, targetBranch string, versions []*codeup.PatchSet) (string, error) {
	var target *codeup.PatchSet
	for _, p := range versions {
		if p.RelatedMergeItemType == "MERGE_TARGET" && (target == nil || p.VersionNo > target.VersionNo) {
			target = p
		}
	}
	if target != nil && target.CommitID != "" {
		return target.CommitID, nil
	}
	branches, err := s.cu.ListBranches(ctx, repository)
	if err != nil {
		return "", err
	}
	for _, b := range branches {
		if b.Name == targetBranch && b.Commit.ID != "" {
			return b.Commit.ID, nil
		}
	}
	return "", fmt.Errorf("MR 目标分支 %s 没有可固定的提交 SHA", targetBranch)
}

func roleNames(agents []agent.Agent) string {
	var names []string
	for _, a := range agents {
		names = append(names, a.Role())
	}
	return strings.Join(names, ",")
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
