// review 集成测试：httptest 模拟 Codeup OpenAPI 与 kimi LLM，
// 端到端验证"提交分支即审查"与"Codeup MR 审查"的 8 条验收标准：
//  1. 首次发现分支仅记录基线   2. 增量触发审查
//  3. 同一提交去重             4. 失败不推进基线
//  5. dry_run 双保险           6. 评论回写与 SHA 存证
//  7. 分支过滤                 8. 原有链路回归（本地 diff）
// 另覆盖覆盖率拦截：文件数超限时审查失败（不发评论、不推进基线、不调用 LLM）。
package review

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/diff"
	"gvs-pr-agent/internal/notifier"
	"gvs-pr-agent/internal/state"
)

// sampleDiff 模拟 Codeup compare 返回的单文件 diff 片段（hunk 覆盖新文件 1-4 行）。
const sampleDiff = "--- a/gvs/main.go\n+++ b/gvs/main.go\n@@ -1,3 +1,4 @@\n package main\n-var key = \"old\"\n+var key = \"hardcoded-secret\"\n+var extra = 1\n"

// fileDiff 生成单文件最小 diff 片段（覆盖率拦截测试用，每个文件一个条目）。
func fileDiff(path string) string {
	return "--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-old\n+new\n"
}

// mockCodeup 有状态的 Codeup OpenAPI 模拟服务。
type mockCodeup struct {
	mu             sync.Mutex
	branches       []map[string]any         // ListBranches 返回
	compareFail    bool                     // GetCompare 返回 500
	compareCalls   []string                 // 记录 "from..to"
	compareDiffs   []map[string]any         // GetCompare 返回的 diffs（nil 时用 sampleDiff 兜底）
	commitComments map[string][]string      // sha -> 评论正文
	mrs            []map[string]any         // ListOpenChangeRequests 返回
	patchSets      []map[string]any         // ListPatchSets 返回
	mrComments     map[int][]map[string]any // localId -> 评论请求体
	knowledgeDoc   string
	fileWrites     int
	srv            *httptest.Server
}

func newMockCodeup(t *testing.T) *mockCodeup {
	t.Helper()
	m := &mockCodeup{
		commitComments: map[string][]string{},
		mrComments:     map[int][]map[string]any{},
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockCodeup) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := r.URL.Path
	switch {
	case strings.Contains(path, "/files/") && r.Method == http.MethodGet:
		if m.knowledgeDoc == "" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": m.knowledgeDoc, "encoding": "text"})

	case (strings.HasSuffix(path, "/files") && r.Method == http.MethodPost) ||
		(strings.Contains(path, "/files/") && r.Method == http.MethodPut):
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		m.knowledgeDoc, _ = payload["content"].(string)
		m.fileWrites++
		w.WriteHeader(http.StatusOK)

	case strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/comments"):
		// POST commit 评论：.../commits/{sha}/comments
		parts := strings.Split(path, "/")
		sha := parts[len(parts)-2]
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		m.commitComments[sha] = append(m.commitComments[sha], payload["content"].(string))
		w.WriteHeader(http.StatusCreated)

	case strings.Contains(path, "/changeRequests/") && strings.HasSuffix(path, "/comments"):
		// POST MR 评论：.../changeRequests/{localId}/comments
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		m.mrComments[8] = append(m.mrComments[8], payload)
		w.WriteHeader(http.StatusOK)

	case strings.Contains(path, "/diffs/patches"):
		_ = json.NewEncoder(w).Encode(m.patchSets)

	case strings.HasSuffix(path, "/changeRequests"):
		_ = json.NewEncoder(w).Encode(m.mrs)

	case strings.HasSuffix(path, "/branches"):
		_ = json.NewEncoder(w).Encode(m.branches)

	case strings.Contains(path, "/compares"):
		if m.compareFail {
			http.Error(w, `{"message":"internal"}`, http.StatusInternalServerError)
			return
		}
		q := r.URL.Query()
		m.compareCalls = append(m.compareCalls, q.Get("from")+".."+q.Get("to"))
		diffs := m.compareDiffs
		if diffs == nil {
			diffs = []map[string]any{{
				"oldPath": "gvs/main.go", "newPath": "gvs/main.go", "diff": sampleDiff,
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"commits": []map[string]any{{"id": "head", "title": "feat: change"}},
			"diffs":   diffs,
		})

	default:
		http.NotFound(w, r)
	}
}

// setBranch 更新分支列表（简化：master + 一个源分支 + 可选额外分支）。
func (m *mockCodeup) setBranches(branches ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.branches = branches
}

func (m *mockCodeup) setCompareFail(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compareFail = fail
}

// setCompareDiffs 自定义 GetCompare 返回的 diffs（覆盖率拦截等场景）。
func (m *mockCodeup) setCompareDiffs(diffs ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compareDiffs = diffs
}

func (m *mockCodeup) snapshot() (compares []string, commitComments map[string][]string, mrComments map[int][]map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	compares = append([]string(nil), m.compareCalls...)
	commitComments = map[string][]string{}
	for k, v := range m.commitComments {
		commitComments[k] = append([]string(nil), v...)
	}
	mrComments = map[int][]map[string]any{}
	for k, v := range m.mrComments {
		mrComments[k] = append([]map[string]any(nil), v...)
	}
	return
}

func branch(name, sha string, isDefault bool) map[string]any {
	return map[string]any{
		"name": name, "defaultBranch": isDefault,
		"commit": map[string]any{"id": sha, "title": "commit title", "authorName": "张腾予"},
	}
}

// newMockLLM 模拟 kimi /chat/completions：固定返回一条 security 问题并计数。
func newMockLLM(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		content := `[{"severity":"high","file":"gvs/main.go","line":2,"problem":"硬编码密钥","detail":"key 变量硬编码了密钥","suggestion":"改用环境变量注入"}]`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
			"usage":   map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// testConfig 构造指向 mock 服务的最小可用配置。
func testConfig(t *testing.T, codeupURL, llmURL string) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.LLM = config.LLMConfig{
		APIKey:  "test-key",
		BaseURL: llmURL,
		Models:  map[string]string{"small": "m-small", "medium": "m-medium", "large": "m-large"},
		Timeout: "5s",
	}
	cfg.LLM.Retry.MaxAttempts = 1
	cfg.Agents = map[string]config.Agent{"security": {Enabled: true, Priority: 1}}
	cfg.Review.MaxDiffChars = 60000
	cfg.Review.MaxAgentConcurrency = 1
	cfg.Review.MaxFilesPerReview = 20
	cfg.Notification.MaxIssuesDisplay = 10
	cfg.Notification.MentionAuthor = true
	cfg.Codeup = config.CodeupConfig{
		Enabled:        true,
		Domain:         codeupURL,
		OrganizationID: "org",
		Token:          "tok",
		PerPage:        100,
		StatePath:      filepath.Join(t.TempDir(), "branch-state.json"),
		CommitComment:  true,
		Repos: []config.CodeupRepoConfig{{
			RepositoryID: "lzvsg/gvs",
			Name:         "lzvsg/gvs",
			BaseBranch:   "master",
			BranchFilter: config.BranchFilter{Exclude: []string{"release/*"}},
		}},
	}
	return cfg
}

func newTestService(t *testing.T, cfg *config.Config) *Service {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := NewService(cfg, "", log)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func baseline(t *testing.T, path, key string) string {
	t.Helper()
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Get(key)
}

// TestBranchPushReviewFlow 覆盖验收标准 1/2/3/6/7：
// 首次基线 -> 去重 -> 增量审查 -> commit 评论存证 -> 分支过滤。
func TestBranchPushReviewFlow(t *testing.T) {
	cu := newMockCodeup(t)
	llmSrv, llmCalls := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
	svc := newTestService(t, cfg)
	ctx := context.Background()
	stateKey := "lzvsg/gvs:qatool-zhangty"

	// 轮次 1：首次发现分支 -> 仅记录基线（标准 1）；release/* 与默认分支被过滤（标准 7）
	cu.setBranches(
		branch("master", "sha-m", true),
		branch("qatool-zhangty", "sha-1", false),
		branch("release/hotfix", "sha-r", false),
	)
	svc.RunOnce(ctx)
	compares, comments, _ := cu.snapshot()
	if len(compares) != 0 || len(comments) != 0 || *llmCalls != 0 {
		t.Fatalf("首次发现应零审查: compares=%v comments=%v llm=%d", compares, comments, *llmCalls)
	}
	if got := baseline(t, cfg.Codeup.StatePath, stateKey); got != "sha-1" {
		t.Fatalf("基线 = %q, want sha-1", got)
	}
	if got := baseline(t, cfg.Codeup.StatePath, "lzvsg/gvs:release/hotfix"); got != "" {
		t.Fatalf("排除分支不应记录基线: %q", got)
	}

	// 轮次 2：HEAD 未变 -> 零 API 审查调用（标准 3）
	svc.RunOnce(ctx)
	compares, comments, _ = cu.snapshot()
	if len(compares) != 0 || len(comments) != 0 || *llmCalls != 0 {
		t.Fatalf("HEAD 未变应零审查: compares=%v comments=%v llm=%d", compares, comments, *llmCalls)
	}

	// 轮次 3：推送新提交 sha-2 -> 增量审查 基线..HEAD 并发布 commit 评论（标准 2、6）
	cu.setBranches(
		branch("master", "sha-m", true),
		branch("qatool-zhangty", "sha-2", false),
		branch("release/hotfix", "sha-r2", false), // 排除分支变更也不审（标准 7）
	)
	svc.RunOnce(ctx)
	compares, comments, _ = cu.snapshot()
	if len(compares) != 1 || compares[0] != "sha-1..sha-2" {
		t.Fatalf("compare 调用 = %v, want [sha-1..sha-2]", compares)
	}
	if *llmCalls == 0 {
		t.Fatal("LLM 未被调用，审查链路未走通")
	}
	bodies := comments["sha-2"]
	if len(bodies) != 1 {
		t.Fatalf("commit 评论 = %v, want sha-2 下 1 条", comments)
	}
	body := bodies[0]
	if !strings.Contains(body, notifier.Marker("sha-2")) {
		t.Error("评论缺少 SHA 存证标记")
	}
	if !strings.Contains(body, "AI 代码审查报告") || !strings.Contains(body, "硬编码密钥") {
		t.Errorf("评论内容不完整:\n%s", body)
	}
	if !strings.Contains(body, "@张腾予") {
		t.Error("评论缺少 @提交人")
	}
	if got := baseline(t, cfg.Codeup.StatePath, stateKey); got != "sha-2" {
		t.Fatalf("审查后基线 = %q, want sha-2", got)
	}

	// 轮次 4：再次运行 -> 已审提交不重复审（标准 3）
	svc.RunOnce(ctx)
	compares, comments, _ = cu.snapshot()
	if len(compares) != 1 || len(comments["sha-2"]) != 1 {
		t.Fatalf("重复轮次不应重复审查: compares=%v comments=%v", compares, comments)
	}
}

// TestFailureKeepsBaseline 覆盖验收标准 4：审查失败不推进基线，恢复后自动重试。
func TestFailureKeepsBaseline(t *testing.T) {
	cu := newMockCodeup(t)
	llmSrv, _ := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
	svc := newTestService(t, cfg)
	ctx := context.Background()
	stateKey := "lzvsg/gvs:qatool-zhangty"

	// 建立基线 sha-1
	cu.setBranches(branch("qatool-zhangty", "sha-1", false))
	svc.RunOnce(ctx)

	// 推送 sha-2 但 compare 接口故障 -> 基线保持 sha-1
	cu.setBranches(branch("qatool-zhangty", "sha-2", false))
	cu.setCompareFail(true)
	svc.RunOnce(ctx)
	if got := baseline(t, cfg.Codeup.StatePath, stateKey); got != "sha-1" {
		t.Fatalf("失败后基线 = %q, want sha-1（不推进）", got)
	}
	_, comments, _ := cu.snapshot()
	if len(comments) != 0 {
		t.Fatalf("失败时不应发布评论: %v", comments)
	}

	// 故障恢复 -> 下一轮自动重试并推进基线
	cu.setCompareFail(false)
	svc.RunOnce(ctx)
	if got := baseline(t, cfg.Codeup.StatePath, stateKey); got != "sha-2" {
		t.Fatalf("恢复后基线 = %q, want sha-2", got)
	}
	_, comments, _ = cu.snapshot()
	if len(comments["sha-2"]) != 1 {
		t.Fatalf("恢复后应补发评论: %v", comments)
	}
}

// TestDryRunNoComment 覆盖验收标准 5：dry_run 与 commit_comment 双开关均可禁止回写。
func TestDryRunNoComment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"dry_run", func(c *config.Config) { c.Review.DryRun = true }},
		{"commit_comment_off", func(c *config.Config) { c.Codeup.CommitComment = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cu := newMockCodeup(t)
			llmSrv, llmCalls := newMockLLM(t)
			cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
			tc.mutate(cfg)
			svc := newTestService(t, cfg)
			ctx := context.Background()

			cu.setBranches(branch("qatool-zhangty", "sha-1", false))
			svc.RunOnce(ctx) // 基线
			cu.setBranches(branch("qatool-zhangty", "sha-2", false))
			svc.RunOnce(ctx) // 审查但不回写

			if *llmCalls == 0 {
				t.Fatal("模拟审查仍应执行 LLM 审查")
			}
			_, comments, _ := cu.snapshot()
			if len(comments) != 0 {
				t.Fatalf("禁用回写时不应发布评论: %v", comments)
			}
			// 模拟审查完成后基线正常推进
			if got := baseline(t, cfg.Codeup.StatePath, "lzvsg/gvs:qatool-zhangty"); got != "sha-2" {
				t.Fatalf("基线 = %q, want sha-2", got)
			}
		})
	}
}

// TestMRReviewFlow 覆盖 Codeup 合并请求审查：版本去重 -> diff 审查 -> MR 全局评论 -> 新版本重审。
func TestMRReviewFlow(t *testing.T) {
	cu := newMockCodeup(t)
	llmSrv, _ := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
	cfg.Codeup.MRReview = true
	cfg.Codeup.MRComment = true
	svc := newTestService(t, cfg)
	ctx := context.Background()

	cu.setBranches(branch("master", "sha-m", true)) // 无待审分支，聚焦 MR
	cu.mu.Lock()
	cu.mrs = []map[string]any{
		{
			"localId": 8, "title": "feat: qatool 改造", "state": "UNDER_REVIEW",
			"sourceBranch": "qatool-zhangty", "targetBranch": "master",
			"author": map[string]any{"name": "张腾予", "username": "zhangty"},
		},
		{ // 源分支命中 exclude（release/*）：应被过滤，不产生 compare/评论
			"localId": 9, "title": "hotfix", "state": "UNDER_DEV",
			"sourceBranch": "release/hotfix", "targetBranch": "master",
			"author": map[string]any{"name": "其他人", "username": "other"},
		},
	}
	cu.patchSets = []map[string]any{
		{"patchSetBizId": "ps-t", "commitId": "sha-m", "versionNo": 1, "relatedMergeItemType": "MERGE_TARGET"},
		{"patchSetBizId": "ps-1", "commitId": "mr-sha-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"},
	}
	cu.mu.Unlock()

	// 轮次 1：审查 MR 并发布全局评论（diff 取 master..qatool-zhangty）
	svc.RunOnce(ctx)
	compares, _, mrComments := cu.snapshot()
	if len(compares) != 1 || compares[0] != "sha-m..mr-sha-1" {
		t.Fatalf("MR compare = %v, want [sha-m..mr-sha-1]", compares)
	}
	if len(mrComments[8]) != 1 {
		t.Fatalf("MR 评论 = %v, want 1 条", mrComments)
	}
	c := mrComments[8][0]
	if c["comment_type"] != "GLOBAL_COMMENT" || c["patchset_biz_id"] != "ps-1" {
		t.Errorf("MR 评论参数 = %+v", c)
	}
	if body := c["content"].(string); !strings.Contains(body, notifier.Marker("mr-sha-1")) {
		t.Error("MR 评论缺少 SHA 存证标记")
	}

	// 轮次 2：合并源版本未变 -> 去重不重审
	svc.RunOnce(ctx)
	compares, _, mrComments = cu.snapshot()
	if len(compares) != 1 || len(mrComments[8]) != 1 {
		t.Fatalf("版本未变不应重审: compares=%v comments=%d", compares, len(mrComments[8]))
	}

	// 轮次 3：MR 推送新版本 v2 -> 重新审查
	cu.mu.Lock()
	cu.patchSets = append(cu.patchSets, map[string]any{
		"patchSetBizId": "ps-2", "commitId": "mr-sha-2", "versionNo": 2, "relatedMergeItemType": "MERGE_SOURCE",
	})
	cu.mu.Unlock()
	svc.RunOnce(ctx)
	compares, _, mrComments = cu.snapshot()
	if len(compares) != 2 || len(mrComments[8]) != 2 {
		t.Fatalf("新版本应重审: compares=%v comments=%d", compares, len(mrComments[8]))
	}
	if c := mrComments[8][1]; c["patchset_biz_id"] != "ps-2" {
		t.Errorf("第二次评论应关联 ps-2: %+v", c)
	}
}

// TestAllAgentsFailedNoPublish 验收标准 4/6 补强（源自真实演练缺陷）：
// LLM 全部失败（如 401）时不得发布"审查通过"空报告，且去重基线不推进。
func TestAllAgentsFailedNoPublish(t *testing.T) {
	cu := newMockCodeup(t)
	badLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"Invalid Authentication"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(badLLM.Close)
	cfg := testConfig(t, cu.srv.URL, badLLM.URL)
	svc := newTestService(t, cfg)
	ctx := context.Background()
	stateKey := "lzvsg/gvs:qatool-zhangty"

	// 建立基线后推送新提交，但 LLM 全部 401
	cu.setBranches(branch("qatool-zhangty", "sha-1", false))
	svc.RunOnce(ctx)
	cu.setBranches(branch("qatool-zhangty", "sha-2", false))
	svc.RunOnce(ctx)

	_, comments, _ := cu.snapshot()
	if len(comments) != 0 {
		t.Fatalf("全部 Agent 失败不应发布评论: %v", comments)
	}
	if got := baseline(t, cfg.Codeup.StatePath, stateKey); got != "sha-1" {
		t.Fatalf("全部失败后基线 = %q, want sha-1（不推进，下轮重试）", got)
	}
}

// TestBranchPushRejectsIncompleteCoverage 覆盖率拦截（分支推送路径）：
// 筛选后文件数超过 max_files_per_review 时审查必须失败——不发评论、不推进基线、不调用 LLM。
func TestBranchPushRejectsIncompleteCoverage(t *testing.T) {
	cu := newMockCodeup(t)
	llmSrv, llmCalls := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
	cfg.Review.MaxFilesPerReview = 1
	svc := newTestService(t, cfg)
	ctx := context.Background()
	stateKey := "lzvsg/gvs:qatool-zhangty"

	cu.setBranches(branch("qatool-zhangty", "sha-1", false))
	svc.RunOnce(ctx) // 首次记录基线

	// 推送 sha-2：compare 返回 2 个文件，超出上限 1
	cu.setCompareDiffs(
		map[string]any{"oldPath": "gvs/a.go", "newPath": "gvs/a.go", "diff": fileDiff("gvs/a.go")},
		map[string]any{"oldPath": "gvs/b.go", "newPath": "gvs/b.go", "diff": fileDiff("gvs/b.go")},
	)
	cu.setBranches(branch("qatool-zhangty", "sha-2", false))
	svc.RunOnce(ctx)

	_, comments, _ := cu.snapshot()
	if len(comments) != 0 {
		t.Fatalf("覆盖不完整时不应发布评论: %v", comments)
	}
	if got := baseline(t, cfg.Codeup.StatePath, stateKey); got != "sha-1" {
		t.Fatalf("覆盖不完整时基线 = %q, want sha-1（不推进，下轮重试）", got)
	}
	if *llmCalls != 0 {
		t.Fatalf("覆盖不完整时不应调用 LLM（避免把部分覆盖当完整结论）, calls=%d", *llmCalls)
	}
}

// TestMRReviewRejectsIncompleteCoverage 覆盖率拦截（MR 路径）：
// 文件数超限时不得发布 MR 评论、不得推进 MR 去重基线。
func TestMRReviewRejectsIncompleteCoverage(t *testing.T) {
	cu := newMockCodeup(t)
	llmSrv, llmCalls := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
	cfg.Review.MaxFilesPerReview = 1
	cfg.Codeup.MRReview = true
	cfg.Codeup.MRComment = true
	svc := newTestService(t, cfg)
	ctx := context.Background()

	cu.setBranches(branch("master", "sha-m", true)) // 无待审分支，聚焦 MR
	cu.mu.Lock()
	cu.mrs = []map[string]any{{
		"localId": 8, "title": "feat: big change", "state": "UNDER_REVIEW",
		"sourceBranch": "feature-big", "targetBranch": "master",
		"author": map[string]any{"name": "张腾予", "username": "zhangty"},
	}}
	cu.patchSets = []map[string]any{
		{"patchSetBizId": "ps-1", "commitId": "mr-sha-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"},
	}
	cu.mu.Unlock()
	cu.setCompareDiffs(
		map[string]any{"oldPath": "gvs/a.go", "newPath": "gvs/a.go", "diff": fileDiff("gvs/a.go")},
		map[string]any{"oldPath": "gvs/b.go", "newPath": "gvs/b.go", "diff": fileDiff("gvs/b.go")},
	)

	svc.RunOnce(ctx)

	_, _, mrComments := cu.snapshot()
	if len(mrComments[8]) != 0 {
		t.Fatalf("覆盖不完整时不应发布 MR 评论: %v", mrComments)
	}
	if got := baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8"); got != "" {
		t.Fatalf("覆盖不完整时 MR 基线 = %q, want 空（不推进，拆分后重审）", got)
	}
	if *llmCalls != 0 {
		t.Fatalf("覆盖不完整时不应调用 LLM, calls=%d", *llmCalls)
	}
}

// TestLocalDiffRegression 覆盖验收标准 8：原有本地 diff 审查链路不受影响。
func TestLocalDiffRegression(t *testing.T) {
	llmSrv, _ := newMockLLM(t)
	cfg := testConfig(t, "http://127.0.0.1:1", llmSrv.URL) // codeup 不可达也不影响本地模式
	svc := newTestService(t, cfg)

	rawDiff := "diff --git a/gvs/main.go b/gvs/main.go\n" + sampleDiff
	report, err := svc.ReviewLocalDiff(context.Background(), rawDiff, "本地回归")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "硬编码密钥") {
		t.Errorf("本地审查报告不含预期问题:\n%s", report)
	}
}

func TestLocalDiffRejectsIncompleteCoverage(t *testing.T) {
	llmSrv, _ := newMockLLM(t)
	cfg := testConfig(t, "http://127.0.0.1:1", llmSrv.URL)
	cfg.Review.MaxFilesPerReview = 1
	svc := newTestService(t, cfg)

	rawDiff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old-a\n+new-a\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old-b\n+new-b\n"
	_, err := svc.ReviewLocalDiff(context.Background(), rawDiff, "超限变更")
	if err == nil || !strings.Contains(err.Error(), "审查覆盖不完整") {
		t.Fatalf("超出文件上限时应拒绝部分评审，err=%v", err)
	}
}

func TestMRReviewUsesUnderstandingAndFinalConfirmation(t *testing.T) {
	cu := newMockCodeup(t)
	var llmCalls int
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		llmCalls++
		system := req.Messages[0].Content
		var content string
		switch {
		case strings.Contains(system, "全局代码理解维护 Agent"):
			content = "# GVS 全局代码理解\n\n- qatool 负责质量检测。"
		case strings.Contains(system, "代码变更意图分析器"):
			content = `{"summary":"调整 qatool 校验","intent":"验证测试流程","affected_modules":["qatool"],"behavior_constraints":["保持退出码语义"],"comment_logic_mismatches":[]}`
		case strings.Contains(system, "最终代码审查统筹确认 Agent"):
			var candidates []struct {
				ID string `json:"candidate_id"`
			}
			index := strings.LastIndex(req.Messages[1].Content, "## 待确认候选问题\n")
			if index < 0 {
				t.Error("missing confirmation candidates")
				return
			}
			json.Unmarshal([]byte(req.Messages[1].Content[index+len("## 待确认候选问题\n"):]), &candidates)
			b, _ := json.Marshal(map[string]any{"overview": "确认1个", "decisions": []map[string]any{{"candidate_id": candidates[0].ID, "confirmed": true, "reason": "diff证据一致"}}})
			content = string(b)
		default:
			content = `[{"severity":"high","file":"gvs/main.go","line":2,"problem":"硬编码密钥","detail":"变量值直接包含凭据","suggestion":"改用环境变量","code":""}]`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	t.Cleanup(llmSrv.Close)

	cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
	cfg.Codeup.MRReview, cfg.Codeup.MRComment = true, true
	cfg.Understanding = config.UnderstandingConfig{
		Enabled: true, SourceRepository: "lzvsg/gvs", SourceBranch: "main",
		DocumentPath: filepath.Join(t.TempDir(), "docs", "gvs-code-understanding.md"),
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Understanding.DocumentPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Understanding.DocumentPath, []byte("<!-- SOURCE-SHA: old-main -->\n# versioned previous knowledge"), 0600); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, cfg)
	cu.setBranches(branch("main", "main-sha-1", true))
	cu.mu.Lock()
	cu.mrs = []map[string]any{{
		"localId": 8, "title": "fix(qatool): test测试", "state": "UNDER_REVIEW",
		"sourceBranch": "qatool-test", "targetBranch": "main",
		"author": map[string]any{"name": "提交人", "username": "author"},
	}}
	cu.patchSets = []map[string]any{{
		"patchSetBizId": "ps-1", "commitId": "mr-sha-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE",
	}}
	cu.mu.Unlock()

	svc.RunOnce(context.Background())
	_, _, comments := cu.snapshot()
	if llmCalls != 4 {
		t.Fatalf("LLM calls = %d, want 4 (document + intent + specialist + confirmer)", llmCalls)
	}
	localDoc, err := os.ReadFile(cfg.Understanding.DocumentPath)
	if err != nil {
		t.Fatalf("read local understanding document: %v", err)
	}
	if cu.fileWrites != 0 || !strings.Contains(string(localDoc), "SOURCE-SHA: main-sha-1") {
		t.Fatalf("remote writes=%d local doc=%q", cu.fileWrites, localDoc)
	}
	body := comments[8][0]["content"].(string)
	for _, want := range []string{"本次代码变更总结", "验证总览", "统筹确认总览", "确认的问题", "硬编码密钥"} {
		if !strings.Contains(body, want) {
			t.Fatalf("comment missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "候选硬编码密钥") {
		t.Fatalf("unconfirmed candidate leaked into comment:\n%s", body)
	}
}

func TestAnalysisDiffUsesOnlyReviewedFilesAndBudget(t *testing.T) {
	parsed := diff.Parse("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old-b\n+new-b\n")
	got := analysisDiff(parsed[:1], 48)
	if strings.Contains(got, "b.go") {
		t.Fatalf("analysis diff leaked unreviewed file: %q", got)
	}
	if len(got) > 48+len("\n...[全局理解 diff 达到预算，已截断]") || !strings.Contains(got, "diff") {
		t.Fatalf("analysis diff budget not applied: len=%d %q", len(got), got)
	}
}
