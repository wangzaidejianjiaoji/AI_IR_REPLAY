package understanding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gvs-pr-agent/internal/codeup"
	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
)

type fakeRepo struct {
	head string
	diff string
}

func (f *fakeRepo) ListBranches(context.Context, string) ([]*codeup.Branch, error) {
	b := &codeup.Branch{Name: "main"}
	b.Commit.ID = f.head
	return []*codeup.Branch{b}, nil
}
func (f *fakeRepo) GetCompareDiff(context.Context, string, string, string) (string, []codeup.CommitInfo, error) {
	return f.diff, nil, nil
}

type fakeModel struct {
	calls  int
	system string
	user   string
}

func (f *fakeModel) Chat(_ context.Context, _ string, messages []llm.Message) (string, llm.Usage, error) {
	f.calls++
	if len(messages) > 0 {
		f.system = messages[0].Content
	}
	if len(messages) > 1 {
		f.user = messages[1].Content
	}
	return "# GVS 全局代码理解\n\n## 模块\n- qatool：质量检测工具。", llm.Usage{TotalTokens: 20}, nil
}

func TestPrepareUsesConfiguredRepositoryInPromptAndMarker(t *testing.T) {
	cfg := understandingConfig(t)
	cfg.SourceRepository = "lizheng/lzvsg/gvs_infer"
	cfg.DocumentPath = filepath.Join(t.TempDir(), "docs", "gvs-infer-code-understanding.md")
	model := &fakeModel{}
	m := New(cfg, &fakeRepo{head: "infer-sha-1"}, model, "infer seed", nil)
	m.readSnapshot = func(_ context.Context, sha string) (string, error) {
		return "SOURCE-SNAPSHOT-SHA: " + sha + "\nFILE: qatool/a.go\npackage qatool\n", nil
	}

	if _, _, err := m.Prepare(context.Background(), "model"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.DocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.system, "gvs_infer") ||
		!strings.Contains(string(raw), "SOURCE-REPOSITORY: lizheng/lzvsg/gvs_infer") ||
		!strings.Contains(string(raw), "SOURCE-SHA: infer-sha-1") {
		t.Fatalf("system=%q doc=%q", model.system, raw)
	}
}

func understandingConfig(t *testing.T) config.UnderstandingConfig {
	t.Helper()
	return config.UnderstandingConfig{
		Enabled: true, SourceRepository: "lzvsg/gvs", SourceBranch: "main",
		DocumentPath: filepath.Join(t.TempDir(), "docs", "gvs-code-understanding.md"),
	}
}

func TestPrepareReusesDocumentWhenMainIsUnchanged(t *testing.T) {
	cfg := understandingConfig(t)
	if err := os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.DocumentPath, []byte("<!-- SOURCE-REPOSITORY: "+cfg.SourceRepository+" -->\n<!-- GVS-SOURCE-SHA: sha-1 -->\n# GVS 全局代码理解"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{head: "sha-1"}
	model := &fakeModel{}
	m := New(cfg, repo, model, "seed", nil)
	m.readSnapshot = func(_ context.Context, sha string) (string, error) {
		return "SOURCE-SNAPSHOT-SHA: " + sha + "\nFILE: qatool/a.go\npackage qatool\n", nil
	}

	got, _, err := m.Prepare(context.Background(), "model")
	if err != nil {
		t.Fatal(err)
	}
	if got.Updated || got.SourceSHA != "sha-1" || model.calls != 0 {
		t.Fatalf("snapshot=%+v model.calls=%d", got, model.calls)
	}
}

func TestPrepareUpdatesDocumentOnceWhenMainChanges(t *testing.T) {
	cfg := understandingConfig(t)
	if err := os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.DocumentPath, []byte("<!-- SOURCE-REPOSITORY: "+cfg.SourceRepository+" -->\n<!-- GVS-SOURCE-SHA: sha-1 -->\n# old"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{
		head: "sha-2",
		diff: "diff --git a/qatool/a.go b/qatool/a.go\n+new",
	}
	model := &fakeModel{}
	m := New(cfg, repo, model, "seed", nil)
	m.readSnapshot = func(_ context.Context, sha string) (string, error) {
		return "SOURCE-SNAPSHOT-SHA: " + sha + "\nFILE: qatool/a.go\npackage qatool\n", nil
	}

	got, usage, err := m.Prepare(context.Background(), "model")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Updated || got.SourceSHA != "sha-2" || usage.TotalTokens != 20 {
		t.Fatalf("snapshot=%+v usage=%+v", got, usage)
	}
	raw, err := os.ReadFile(cfg.DocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || !strings.Contains(string(raw), "SOURCE-SHA: sha-2") {
		t.Fatalf("model.calls=%d doc=%q", model.calls, raw)
	}
	if _, _, err := m.Prepare(context.Background(), "model"); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("same main SHA must not update twice: calls=%d", model.calls)
	}
}

func TestPrepareCreatesMissingDocumentFromSeed(t *testing.T) {
	cfg := understandingConfig(t)
	repo := &fakeRepo{head: "sha-1"}
	model := &fakeModel{}
	m := New(cfg, repo, model, "GVS 项目包含 qatool 和跟踪模块", nil)
	m.readSnapshot = func(_ context.Context, sha string) (string, error) {
		return "SOURCE-SNAPSHOT-SHA: " + sha + "\nFILE: qatool/a.go\npackage qatool\n", nil
	}
	got, _, err := m.Prepare(context.Background(), "model")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.DocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Updated || !strings.Contains(string(raw), "sha-1") {
		t.Fatalf("snapshot=%+v doc=%q", got, raw)
	}
}

func TestPrepareStoresUnderstandingOnlyInLocalProject(t *testing.T) {
	cfg := understandingConfig(t)
	cfg.DocumentPath = filepath.Join(t.TempDir(), "docs", "gvs-code-understanding.md")
	repo := &fakeRepo{head: "sha-local-1"}
	model := &fakeModel{}
	m := New(cfg, repo, model, "GVS 初始化材料", nil)
	m.readSnapshot = func(_ context.Context, sha string) (string, error) {
		return "SOURCE-SNAPSHOT-SHA: " + sha + "\nFILE: qatool/a.go\npackage qatool\n", nil
	}

	got, _, err := m.Prepare(context.Background(), "model")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.DocumentPath)
	if err != nil {
		t.Fatalf("理解文档应写入本地项目路径 %s: %v", cfg.DocumentPath, err)
	}
	if !strings.Contains(string(raw), "SOURCE-SHA: sha-local-1") || !got.Updated {
		t.Fatalf("local document=%q snapshot=%+v", raw, got)
	}
}

func TestPrepareInitialisesUnversionedLocalDocumentFromSeed(t *testing.T) {
	cfg := understandingConfig(t)
	if err := os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.DocumentPath, []byte("# GVS 代码理解\n\n等待首次同步。"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	m := New(cfg, &fakeRepo{head: "sha-first"}, model, "唯一初始化材料", nil)
	m.readSnapshot = func(_ context.Context, sha string) (string, error) {
		return "SOURCE-SNAPSHOT-SHA: " + sha + "\nFILE: qatool/a.go\npackage qatool\n", nil
	}

	if _, _, err := m.Prepare(context.Background(), "model"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.user, "唯一初始化材料") {
		t.Fatalf("首次同步必须使用 seed，而不是把占位文档当成既有理解：%q", model.user)
	}
}
