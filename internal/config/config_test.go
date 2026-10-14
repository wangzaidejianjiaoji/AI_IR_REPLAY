// config codeup 段单元测试：加载、默认值、校验规则。
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const codeupYAML = `
llm:
  api_key: "test-key"
codeup:
  enabled: true
  organization_id: "org-1"
  token: "tok-1"
  repos:
    - repository_id: "lzvsg/gvs"
      base_branch: "master"
      branch_filter:
        include: ["qatool-*"]
        exclude: ["release/*"]
  mr_review: true
  mr_comment: true
  commit_comment: true
`

func TestLoadCodeupDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, codeupYAML))
	if err != nil {
		t.Fatal(err)
	}
	cu := cfg.Codeup
	if !cu.Enabled || cu.OrganizationID != "org-1" || cu.Token != "tok-1" {
		t.Errorf("codeup 基础字段 = %+v", cu)
	}
	// 默认值
	if cu.Domain != "openapi-rdc.aliyuncs.com" {
		t.Errorf("domain 默认值 = %s", cu.Domain)
	}
	if cu.PerPage != 100 {
		t.Errorf("per_page 默认值 = %d", cu.PerPage)
	}
	if cu.StatePath != "./data/branch-state.json" {
		t.Errorf("state_path 默认值 = %s", cu.StatePath)
	}
	// 仓库配置
	repo := cu.Repos[0]
	if repo.RepositoryID != "lzvsg/gvs" || repo.BaseBranch != "master" {
		t.Errorf("repo = %+v", repo)
	}
	if repo.DisplayName() != "lzvsg/gvs" { // name 缺省取 repository_id
		t.Errorf("DisplayName = %s", repo.DisplayName())
	}
	if !cu.MRReview || !cu.MRComment || !cu.CommitComment {
		t.Errorf("开关 = mr_review=%v mr_comment=%v commit_comment=%v",
			cu.MRReview, cu.MRComment, cu.CommitComment)
	}
}

func TestSchedulerDefaultIntervalIsOneMinute(t *testing.T) {
	cfg, err := Load(writeConfig(t, codeupYAML))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Scheduler.IntervalDuration(); got != time.Minute {
		t.Fatalf("scheduler interval = %s, want 1m", got)
	}
}

func TestLoadUnderstandingConfig(t *testing.T) {
	cfg, err := Load(writeConfig(t, codeupYAML+`
understanding:
  enabled: true
  source_repository: "lzvsg/gvs"
  source_branch: "main"
  document_path: "docs/gvs-code-understanding.md"
  seed_path: "GO_AI_PR_Review.md"
`))
	if err != nil {
		t.Fatal(err)
	}
	u := cfg.Understanding
	if !u.Enabled || u.SourceRepository != "lzvsg/gvs" || u.SourceBranch != "main" ||
		u.DocumentPath != "docs/gvs-code-understanding.md" || u.SeedPath != "GO_AI_PR_Review.md" {
		t.Fatalf("understanding = %+v", u)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("本地理解文档不应要求远端知识仓库: %v", err)
	}
}

func TestValidateCodeupOnly(t *testing.T) {
	// 只配 codeup 不配 github.repos 应通过校验
	cfg, err := Load(writeConfig(t, codeupYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("仅 codeup 配置应通过校验: %v", err)
	}
}

func TestValidateMissing(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"两者均未配置", `
llm: {api_key: "k"}
`},
		{"codeup 缺组织 ID", `
llm: {api_key: "k"}
codeup:
  enabled: true
  token: "t"
  repos: [{repository_id: "a/b"}]
`},
		{"codeup 缺 token", `
llm: {api_key: "k"}
codeup:
  enabled: true
  organization_id: "org"
  repos: [{repository_id: "a/b"}]
`},
		{"codeup 缺仓库", `
llm: {api_key: "k"}
codeup:
  enabled: true
  organization_id: "org"
  token: "t"
`},
		{"缺 LLM key", `
codeup:
  enabled: true
  organization_id: "org"
  token: "t"
  repos: [{repository_id: "a/b"}]
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("%s: 应校验失败", tc.name)
			}
		})
	}
}
