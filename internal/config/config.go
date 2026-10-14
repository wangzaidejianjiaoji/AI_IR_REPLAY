// Package config 负责加载和解析 Agent 配置文件（YAML），
// 支持 ${ENV_VAR} 环境变量展开与环境变量覆盖。
package config

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 Agent 的顶层配置结构，对应 configs/config.yaml。
type Config struct {
	Scheduler     SchedulerConfig     `yaml:"scheduler"`
	GitHub        GitHubConfig        `yaml:"github"`
	Codeup        CodeupConfig        `yaml:"codeup"`
	LLM           LLMConfig           `yaml:"llm"`
	Agents        map[string]Agent    `yaml:"agents"`
	Review        ReviewConfig        `yaml:"review"`
	Rules         RulesConfig         `yaml:"rules"`
	Understanding UnderstandingConfig `yaml:"understanding"`
	Notification  NotificationConfig  `yaml:"notification"`
	Logging       LoggingConfig       `yaml:"logging"`
	Server        ServerConfig        `yaml:"server"`
}

// SchedulerConfig 定时轮询配置。
type SchedulerConfig struct {
	Interval string `yaml:"interval"`
	Jitter   string `yaml:"jitter"`
}

// IntervalDuration 返回轮询间隔，未配置时默认为 1 分钟。
func (s SchedulerConfig) IntervalDuration() time.Duration {
	return parseDuration(s.Interval, time.Minute)
}

// JitterDuration 返回抖动上限，解析失败时回退为 0。
func (s SchedulerConfig) JitterDuration() time.Duration {
	return parseDuration(s.Jitter, 0)
}

// GitHubConfig GitHub API 相关配置。
type GitHubConfig struct {
	APIBase string       `yaml:"api_base"`
	Token   string       `yaml:"token"`
	Repos   []RepoConfig `yaml:"repos"`
	PerPage int          `yaml:"per_page"`
}

// RepoConfig 单个仓库的审查配置。
type RepoConfig struct {
	Owner        string       `yaml:"owner"`
	Repo         string       `yaml:"repo"`
	BranchFilter BranchFilter `yaml:"branch_filter"`
}

// BranchFilter PR 目标分支过滤规则。
type BranchFilter struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// CodeupConfig 阿里云 Codeup OpenAPI 配置（提交分支即审查）。
// 轮询各仓库分支 HEAD 变化，对新提交做增量审查并回写 commit 评论。
type CodeupConfig struct {
	Enabled         bool               `yaml:"enabled"`         // 是否启用分支推送审查
	Domain          string             `yaml:"domain"`          // 服务接入点（默认 openapi-rdc.aliyuncs.com）
	OrganizationID  string             `yaml:"organization_id"` // 组织 ID（中心版必填）
	Token           string             `yaml:"token"`           // 个人访问令牌（x-yunxiao-token）
	Repos           []CodeupRepoConfig `yaml:"repos"`
	PerPage         int                `yaml:"per_page"`          // 分支列表每页数量
	StatePath       string             `yaml:"state_path"`        // 分支基线存储文件（默认 ./data/branch-state.json）
	ReviewFirstPush bool               `yaml:"review_first_push"` // 首次发现分支时是否对比基准分支做全量审查（默认仅记录基线）
	CommitComment   bool               `yaml:"commit_comment"`    // 是否将审查结果回写为 commit 评论
	MRReview        bool               `yaml:"mr_review"`         // 是否审查 Codeup 合并请求（新版 MR）
	MRComment       bool               `yaml:"mr_comment"`        // 是否将审查结果回写为 MR 全局评论
}

// CodeupRepoConfig 单个 Codeup 仓库的分支审查配置。
type CodeupRepoConfig struct {
	RepositoryID string       `yaml:"repository_id"` // 代码库 ID 或全路径（如 lzvsg/gvs）
	Name         string       `yaml:"name"`          // 展示名（日志/评论用，缺省取 repository_id）
	BaseBranch   string       `yaml:"base_branch"`   // 基准分支（review_first_push 时的对比基线，默认 master）
	BranchFilter BranchFilter `yaml:"branch_filter"` // 源分支过滤（作用于被推送的分支名）
}

// DisplayName 返回仓库展示名。
func (r CodeupRepoConfig) DisplayName() string {
	if r.Name != "" {
		return r.Name
	}
	return r.RepositoryID
}

// LLMConfig kimi/Moonshot LLM 配置。
type LLMConfig struct {
	Provider        string                `yaml:"provider"`
	APIKey          string                `yaml:"api_key"`
	BaseURL         string                `yaml:"base_url"`
	Models          map[string]string     `yaml:"models"`
	Prices          map[string]ModelPrice `yaml:"prices"`
	ModelThresholds struct {
		Small  int `yaml:"small"`
		Medium int `yaml:"medium"`
	} `yaml:"model_thresholds"`
	Timeout     string  `yaml:"timeout"`
	MaxTokens   int     `yaml:"max_tokens"`
	Temperature float64 `yaml:"temperature"`
	Retry       struct {
		MaxAttempts int    `yaml:"max_attempts"`
		BaseDelay   string `yaml:"base_delay"`
		MaxDelay    string `yaml:"max_delay"`
	} `yaml:"retry"`
	// Fallback 备用 LLM 配置，当主模型限额时自动切换
	Fallback *FallbackConfig `yaml:"fallback"`
}

// FallbackConfig 备用模型配置。
type FallbackConfig struct {
	Enabled bool              `yaml:"enabled"` // 是否启用备用模型
	APIKey  string            `yaml:"api_key"` // 备用模型 API Key
	BaseURL string            `yaml:"base_url"` // 备用模型 Base URL
	Models  map[string]string `yaml:"models"`  // 备用模型分级配置
}

type ModelPrice struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
}

// TimeoutDuration 返回单次 LLM 请求超时。
func (l LLMConfig) TimeoutDuration() time.Duration {
	return parseDuration(l.Timeout, 120*time.Second)
}

// RetryBaseDelay 返回重试基础延迟。
func (l LLMConfig) RetryBaseDelay() time.Duration {
	return parseDuration(l.Retry.BaseDelay, 2*time.Second)
}

// RetryMaxDelay 返回重试最大延迟。
func (l LLMConfig) RetryMaxDelay() time.Duration {
	return parseDuration(l.Retry.MaxDelay, 30*time.Second)
}

// SelectModel 按 diff 变更行数选择模型（模型分级策略）。
func (l LLMConfig) SelectModel(changedLines int) string {
	small, medium := l.ModelThresholds.Small, l.ModelThresholds.Medium
	if small <= 0 {
		small = 500
	}
	if medium <= 0 {
		medium = 3000
	}
	switch {
	case changedLines < small:
		return l.Models["small"]
	case changedLines < medium:
		return l.Models["medium"]
	default:
		return l.Models["large"]
	}
}

// Agent 单个 Agent 角色的开关与优先级。
type Agent struct {
	Enabled  bool `yaml:"enabled"`
	Priority int  `yaml:"priority"`
}

// ReviewConfig 审查行为配置。
type ReviewConfig struct {
	DryRun              bool     `yaml:"dry_run"`
	MaxDiffChars        int      `yaml:"max_diff_chars"`
	MaxAgentConcurrency int      `yaml:"max_agent_concurrency"`
	MaxFilesPerReview   int      `yaml:"max_files_per_review"`
	IncludePaths        []string `yaml:"include_paths"`
	ExcludePaths        []string `yaml:"exclude_paths"`
	MaxLLMCalls         int      `yaml:"max_llm_calls"`
	MaxRoundTokens      int      `yaml:"max_round_tokens"`
	RoundTimeout        string   `yaml:"round_timeout"`
	MaxRoundCost        float64  `yaml:"max_round_cost"`

	// 文件打包配置（确定性工程：关联文件归并为同一审查单元）
	FilePackaging struct {
		Enabled        bool `yaml:"enabled"`
		MaxPackageSize int  `yaml:"max_package_size"`
	} `yaml:"file_packaging"`

	// 评论定位配置（确定性工程：行级精度校验）
	CommentLocation struct {
		Enabled          bool `yaml:"enabled"`
		VerifyLineNumber bool `yaml:"verify_line_number"`
	} `yaml:"comment_location"`

	// 评论反思配置（确定性工程：准确性校验 + 跨 Agent 去重）
	CommentReflection struct {
		Enabled            bool `yaml:"enabled"`
		CheckHallucination bool `yaml:"check_hallucination"`
	} `yaml:"comment_reflection"`

	// 审查时间窗口（避免夜间打扰，与 GVS 日夜模式对应）；留空表示不限制
	ActiveHours struct {
		Start    string `yaml:"start"`    // "09:00"
		End      string `yaml:"end"`      // "21:00"
		Timezone string `yaml:"timezone"` // "Asia/Shanghai"
	} `yaml:"active_hours"`
}

// InActiveHours 判断当前时刻是否在审查时间窗口内（未配置时恒为 true）。
func (r ReviewConfig) InActiveHours(now time.Time) bool {
	if r.ActiveHours.Start == "" || r.ActiveHours.End == "" {
		return true
	}
	loc := time.Local
	if r.ActiveHours.Timezone != "" {
		if l, err := time.LoadLocation(r.ActiveHours.Timezone); err == nil {
			loc = l
		}
	}
	start, err1 := time.Parse("15:04", r.ActiveHours.Start)
	end, err2 := time.Parse("15:04", r.ActiveHours.End)
	if err1 != nil || err2 != nil {
		return true
	}
	t := now.In(loc)
	cur := t.Hour()*60 + t.Minute()
	s := start.Hour()*60 + start.Minute()
	e := end.Hour()*60 + end.Minute()
	if s <= e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e // 跨午夜窗口
}

// RulesConfig 规则文档配置（四层优先级链 + 热更新）。
type RulesConfig struct {
	ProjectPath    string `yaml:"project_path"`    // 优先级 2: 项目配置
	GlobalPath     string `yaml:"global_path"`     // 优先级 3: 全局配置（默认 ~/.ai-review/review-rules.yaml）
	HotReload      bool   `yaml:"hot_reload"`      // 是否启用热更新
	ReloadInterval string `yaml:"reload_interval"` // 热更新轮询间隔
}

// UnderstandingConfig 配置每轮全局理解与本地理解文档。
type UnderstandingConfig struct {
	Enabled            bool   `yaml:"enabled"`
	SourceRepository   string `yaml:"source_repository"`
	SourceBranch       string `yaml:"source_branch"`
	DocumentPath       string `yaml:"document_path"`
	SeedPath           string `yaml:"seed_path"`
	Strict             bool   `yaml:"strict"`
	SnapshotRepository string `yaml:"snapshot_repository"` // local Git objects for verified first-time initialization
	MaxSnapshotBytes   int    `yaml:"max_snapshot_bytes"`
}

// ReloadIntervalDuration 返回热更新轮询间隔，默认 30s。
func (r RulesConfig) ReloadIntervalDuration() time.Duration {
	return parseDuration(r.ReloadInterval, 30*time.Second)
}

// NotificationConfig PR 评论通知配置。
type NotificationConfig struct {
	PRComment        bool `yaml:"pr_comment"` // 是否发布 PR 评论（demo 默认禁用，只输出到日志）
	MentionAuthor    bool `yaml:"mention_author"`
	MentionReviewers bool `yaml:"mention_reviewers"`
	MaxIssuesDisplay int  `yaml:"max_issues_display"`
}

// LoggingConfig 日志配置（与 GVS 日志体系一致）。
type LoggingConfig struct {
	Level    string `yaml:"level"`
	Format   string `yaml:"format"`    // json/text/gvs（gvs = gvlogger 的 "[I]" 前缀风格）
	Output   string `yaml:"output"`    // stdout/file/both
	FilePath string `yaml:"file_path"` // 文件输出路径（如 /opt/lz/log/pr-agent.log）
}

// ServerConfig 健康检查/手动触发 HTTP 服务配置。
type ServerConfig struct {
	Addr       string `yaml:"addr"`
	AdminToken string `yaml:"admin_token"`
}

var envVarPattern = regexp.MustCompile(`\$\{(\w+)\}`)

// Load 从指定路径加载配置，展开 ${ENV_VAR} 占位符并应用环境变量覆盖。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// ${ENV_VAR} -> 环境变量值（未设置时展开为空串）
	expanded := envVarPattern.ReplaceAllStringFunc(string(data), func(m string) string {
		name := envVarPattern.FindStringSubmatch(m)[1]
		return os.Getenv(name)
	})

	cfg := &Config{}
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err:=decoder.Decode(new(any));err!=io.EOF{return nil,fmt.Errorf("配置只允许一个 YAML 文档")}

	cfg.applyEnvOverrides()
	if err := cfg.ValidateRuntime(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err:=cfg.ValidateRuntime();err!=nil{return nil,err}
	return cfg, nil
}

// applyEnvOverrides 应用环境变量覆盖（优先级高于配置文件）。
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("AGENT_LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("AGENT_REVIEW_DRY_RUN"); v == "true" || v == "1" {
		c.Review.DryRun = true
	}
	if v := os.Getenv("AGENT_ADMIN_TOKEN"); v != "" {
		c.Server.AdminToken = v
	}
	if v := os.Getenv("AGENT_SERVER_ADDR"); v != "" {
		c.Server.Addr = v
	}
}

// applyDefaults 填充缺省值。
func (c *Config) applyDefaults() {
	if c.GitHub.APIBase == "" {
		c.GitHub.APIBase = "https://api.github.com"
	}
	if c.GitHub.PerPage <= 0 {
		c.GitHub.PerPage = 30
	}
	if c.LLM.BaseURL == "" {
		c.LLM.BaseURL = "https://api.moonshot.cn/v1"
	}
	if c.LLM.MaxTokens <= 0 {
		c.LLM.MaxTokens = 4000
	}
	if c.LLM.Retry.MaxAttempts <= 0 {
		c.LLM.Retry.MaxAttempts = 3
	}
	if c.Review.MaxDiffChars <= 0 {
		c.Review.MaxDiffChars = 60000
	}
	if c.Review.MaxAgentConcurrency <= 0 {
		c.Review.MaxAgentConcurrency = 4
	}
	if c.Review.MaxFilesPerReview <= 0 {
		c.Review.MaxFilesPerReview = 20
	}
	if c.Review.FilePackaging.MaxPackageSize <= 0 {
		c.Review.FilePackaging.MaxPackageSize = 5
	}
	if c.Notification.MaxIssuesDisplay <= 0 {
		c.Notification.MaxIssuesDisplay = 10
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Output == "" {
		c.Logging.Output = "stdout"
	}
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8081"
	}
	if c.Review.MaxLLMCalls == 0 {
		c.Review.MaxLLMCalls = 100
	}
	if c.Review.MaxRoundTokens == 0 {
		c.Review.MaxRoundTokens = 1000000
	}
	if c.Review.RoundTimeout == "" {
		c.Review.RoundTimeout = "20m"
	}
	if c.LLM.Models == nil {
		c.LLM.Models = map[string]string{"small": "moonshot-v1-8k", "medium": "moonshot-v1-32k", "large": "moonshot-v1-128k"}
	}
	if c.Codeup.Domain == "" {
		c.Codeup.Domain = "openapi-rdc.aliyuncs.com"
	}
	if c.Codeup.PerPage <= 0 {
		c.Codeup.PerPage = 100
	}
	if c.Codeup.StatePath == "" {
		c.Codeup.StatePath = "./data/branch-state.json"
	}
	if c.Understanding.SourceBranch == "" {
		c.Understanding.SourceBranch = "main"
	}
	if c.Understanding.DocumentPath == "" {
		c.Understanding.DocumentPath = "docs/gvs-code-understanding.md"
	}
}

// Validate 校验运行必需项（不发布评论时允许缺少 GitHub Token 以便本地演示）。
func (c *Config) Validate() error {
	if err := c.ValidateRuntime(); err != nil {
		return err
	}
	if len(c.GitHub.Repos) == 0 && !c.Codeup.Enabled {
		return fmt.Errorf("github.repos 与 codeup.enabled 至少配置一项")
	}
	if len(c.GitHub.Repos) > 0 && c.GitHub.Token == "" && c.Notification.PRComment && !c.Review.DryRun {
		return fmt.Errorf("缺少 GITHUB_TOKEN（启用评论发布时必填）")
	}
	if c.Codeup.Enabled {
		if c.Codeup.OrganizationID == "" {
			return fmt.Errorf("codeup.organization_id 不能为空（启用分支推送审查时必填）")
		}
		if len(c.Codeup.Repos) == 0 {
			return fmt.Errorf("codeup.repos 不能为空（启用分支推送审查时必填）")
		}
		if c.Codeup.Token == "" {
			return fmt.Errorf("缺少 CODEUP_TOKEN（启用分支推送审查时必填）")
		}
	}
	if c.Understanding.Enabled {
		if !c.Codeup.Enabled {
			return fmt.Errorf("understanding.enabled 需要启用 codeup")
		}
		if c.Understanding.SourceRepository == "" || c.Understanding.DocumentPath == "" {
			return fmt.Errorf("understanding.source_repository 与 document_path 不能为空")
		}
	}
	if c.LLM.APIKey == "" {
		return fmt.Errorf("缺少 KIMI_API_KEY")
	}
	return nil
}

// ValidateRuntime rejects malformed explicit settings, also in local diff mode.
func (c *Config) ValidateRuntime() error {
	if math.IsNaN(c.Review.MaxRoundCost) || math.IsInf(c.Review.MaxRoundCost, 0) || c.Review.MaxRoundCost < 0 {
		return fmt.Errorf("review.max_round_cost 必须为非负有限数")
	}
	for name, p := range c.LLM.Prices {
		if math.IsNaN(p.InputPerMillion) || math.IsInf(p.InputPerMillion, 0) || p.InputPerMillion < 0 || math.IsNaN(p.OutputPerMillion) || math.IsInf(p.OutputPerMillion, 0) || p.OutputPerMillion < 0 {
			return fmt.Errorf("模型 %s 价格无效", name)
		}
	}
	if c.Review.MaxRoundCost > 0 {
		for _, name := range c.LLM.Models {
			if _, ok := c.LLM.Prices[name]; !ok {
				return fmt.Errorf("费用预算需要配置模型 %s 的 llm.prices", name)
			}
		}
	}
	for _, v := range []struct {
		name, value string
		zero        bool
	}{
		{"scheduler.interval", c.Scheduler.Interval, false}, {"scheduler.jitter", c.Scheduler.Jitter, true},
		{"llm.timeout", c.LLM.Timeout, false}, {"llm.retry.base_delay", c.LLM.Retry.BaseDelay, false},
		{"llm.retry.max_delay", c.LLM.Retry.MaxDelay, false}, {"rules.reload_interval", c.Rules.ReloadInterval, false},
		{"review.round_timeout", c.Review.RoundTimeout, false},
	} {
		if v.value != "" {
			d, err := time.ParseDuration(v.value)
			if err != nil || d < 0 || (!v.zero && d == 0) {
				return fmt.Errorf("%s 必须是有效%s时长", v.name, map[bool]string{true: "非负", false: "正"}[v.zero])
			}
		}
	}
	for name, value := range map[string]int{"review.max_agent_concurrency": c.Review.MaxAgentConcurrency, "review.max_files_per_review": c.Review.MaxFilesPerReview, "review.max_diff_chars": c.Review.MaxDiffChars, "review.max_llm_calls": c.Review.MaxLLMCalls, "review.max_round_tokens": c.Review.MaxRoundTokens, "review.file_packaging.max_package_size": c.Review.FilePackaging.MaxPackageSize, "llm.max_tokens": c.LLM.MaxTokens, "llm.retry.max_attempts": c.LLM.Retry.MaxAttempts, "understanding.max_snapshot_bytes": c.Understanding.MaxSnapshotBytes} {
		if value < 0 {
			return fmt.Errorf("%s 不能为负数", name)
		}
	}
	known := map[string]bool{"architect": true, "security": true, "performance": true, "standard": true, "test": true, "ai": true, "ui": true, "ai_tool": true,"concurrency":true,"resource":true,"dataflow":true}
	for role, a := range c.Agents {
		if !known[role] {
			return fmt.Errorf("不支持 Agent 角色 %q", role)
		}
		if a.Priority < 0 {
			return fmt.Errorf("Agent %s priority 不能为负数", role)
		}
	}
	if c.LLM.Models != nil {
		for _, tier := range []string{"small", "medium", "large"} {
			if strings.TrimSpace(c.LLM.Models[tier]) == "" {
				return fmt.Errorf("llm.models.%s 不能为空", tier)
			}
		}
		for tier := range c.LLM.Models {
			if tier != "small" && tier != "medium" && tier != "large" {
				return fmt.Errorf("未知模型档位 %s", tier)
			}
		}
	}
	small, medium := c.LLM.ModelThresholds.Small, c.LLM.ModelThresholds.Medium
	if small < 0 || medium < 0 {
		return fmt.Errorf("模型分级阈值不能为负数")
	}
	if small == 0 {
		small = 500
	}
	if medium == 0 {
		medium = 3000
	}
	if small >= medium {
		return fmt.Errorf("模型分级 small 必须小于 medium")
	}
	if c.LLM.Retry.BaseDelay != "" && c.LLM.Retry.MaxDelay != "" && c.LLM.RetryBaseDelay() > c.LLM.RetryMaxDelay() {
		return fmt.Errorf("重试 base_delay 不能超过 max_delay")
	}
	hours := c.Review.ActiveHours
	if (hours.Start == "") != (hours.End == "") {
		return fmt.Errorf("active_hours start/end 必须成对配置")
	}
	if hours.Start != "" {
		for _, v := range []string{hours.Start, hours.End} {
			if _, err := time.Parse("15:04", v); err != nil {
				return fmt.Errorf("active_hours 时间无效: %s", v)
			}
		}
	}
	if hours.Timezone != "" {
		if _, err := time.LoadLocation(hours.Timezone); err != nil {
			return fmt.Errorf("active_hours timezone 无效: %w", err)
		}
	}
	if c.Server.Addr != "" {
		host, port, err := net.SplitHostPort(c.Server.Addr)
		if err != nil {
			return fmt.Errorf("server.addr 无效: %w", err)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("server.addr 端口无效")
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) && c.Server.AdminToken == "" {
			return fmt.Errorf("非回环管理地址必须配置 server.admin_token")
		}
	}
	return nil
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
