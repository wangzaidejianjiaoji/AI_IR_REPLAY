// Package understanding maintains the single GVS code-understanding document
// stored in the local AI_IR_REPLAY project.
package understanding

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gvs-pr-agent/internal/codeup"
	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
)

var sourceSHARe = regexp.MustCompile(`<!--\s*(?:GVS-)?SOURCE-SHA:\s*([0-9A-Za-z._-]+)\s*-->`)
var sourceRepositoryRe = regexp.MustCompile(`<!--\s*SOURCE-REPOSITORY:\s*(.*?)\s*-->`)

// Repository is the Codeup surface used by Manager.
type Repository interface {
	ListBranches(context.Context, string) ([]*codeup.Branch, error)
	GetCompareDiff(context.Context, string, string, string) (string, []codeup.CommitInfo, error)
}

// ChatClient is the model surface used for document updates.
type ChatClient interface {
	Chat(context.Context, string, []llm.Message) (string, llm.Usage, error)
}

// Snapshot is the knowledge context used by one review.
type Snapshot struct {
	Document  string
	SourceSHA string
	Updated   bool
}

// Manager serialises update checks so multiple reviews of the same main SHA
// cannot rewrite the local document more than once.
type Manager struct {
	cfg          config.UnderstandingConfig
	repo         Repository
	model        ChatClient
	seed         string
	logger       *slog.Logger
	mu           sync.Mutex
	readSnapshot func(context.Context, string) (string, error)
}

func New(cfg config.UnderstandingConfig, repo Repository, model ChatClient, seed string, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, repo: repo, model: model, seed: seed, logger: logger}
}

// Prepare returns the latest understanding document. It updates the local file
// only when gvs/main HEAD differs from the SHA embedded in the document.
func (m *Manager) Prepare(ctx context.Context, modelName string) (Snapshot, llm.Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, exists, err := loadDocument(m.cfg.DocumentPath)
	if err != nil {
		return Snapshot{}, llm.Usage{}, fmt.Errorf("读取本地全局代码理解文档: %w", err)
	}
	repositoryMarkers := sourceRepositoryRe.FindAllStringSubmatch(doc, -1)
	for _, marker := range repositoryMarkers {
		if strings.TrimSpace(marker[1]) != m.cfg.SourceRepository {
			return Snapshot{}, llm.Usage{}, fmt.Errorf("理解文档来源仓库不匹配：配置 %s，文档 %s", m.cfg.SourceRepository, strings.TrimSpace(marker[1]))
		}
	}
	previous := sourceSHA(doc)
	if previous != "" && len(repositoryMarkers) == 0 {
		return Snapshot{}, llm.Usage{}, fmt.Errorf("版本化理解文档缺少 SOURCE-REPOSITORY 来源标记")
	}
	for _, marker := range sourceSHARe.FindAllStringSubmatch(doc, -1) {
		if marker[1] != previous {
			return Snapshot{}, llm.Usage{}, fmt.Errorf("理解文档包含冲突的 SOURCE-SHA 标记")
		}
	}
	fallback := Snapshot{Document: doc, SourceSHA: previous}
	if previous == "" {
		fallback = Snapshot{}
	}
	head, err := m.branchHead(ctx)
	if err != nil {
		return fallback, llm.Usage{}, err
	}
	if exists && previous == head {
		return Snapshot{Document: doc, SourceSHA: head}, llm.Usage{}, nil
	}

	var delta string
	if previous != "" {
		delta, _, err = m.repo.GetCompareDiff(ctx, m.cfg.SourceRepository, previous, head)
		if err != nil {
			return fallback, llm.Usage{}, fmt.Errorf("获取理解增量 %s..%s: %w", short(previous), short(head), err)
		}
		if len(doc) > 60000 || len(delta) > 90000 {
			return fallback, llm.Usage{}, fmt.Errorf("理解更新输入超出预算，保留旧版本，不标记新 SHA")
		}
	} else {
		reader := m.readSnapshot
		if reader == nil {
			reader = func(ctx context.Context, sha string) (string, error) { return gitSnapshot(ctx, m.cfg, sha) }
		}
		delta, err = reader(ctx, head)
		if err != nil {
			return fallback, llm.Usage{}, fmt.Errorf("初始化需要版本化源码快照: %w", err)
		}
	}
	updated, usage, err := m.updateDocument(ctx, modelName, doc, delta)
	if err != nil {
		return fallback, usage, err
	}
	updated = fmt.Sprintf("<!-- SOURCE-REPOSITORY: %s -->\n<!-- SOURCE-SHA: %s -->\n\n%s",
		m.cfg.SourceRepository, head, strings.TrimSpace(updated))
	if err := writeDocument(m.cfg.DocumentPath, updated); err != nil {
		return fallback, usage, fmt.Errorf("写入本地全局代码理解文档: %w", err)
	}
	if m.logger != nil {
		m.logger.Info("本地全局代码理解文档已同步", "source_sha", short(head),
			"path", m.cfg.DocumentPath, "created", !exists)
	}
	return Snapshot{Document: updated, SourceSHA: head, Updated: true}, usage, nil
}

func loadDocument(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return string(raw), true, nil
	}
	if os.IsNotExist(err) {
		return "", false, nil
	}
	return "", false, err
}

func writeDocument(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".code-understanding-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (m *Manager) branchHead(ctx context.Context) (string, error) {
	branches, err := m.repo.ListBranches(ctx, m.cfg.SourceRepository)
	if err != nil {
		return "", fmt.Errorf("读取源仓库分支: %w", err)
	}
	for _, branch := range branches {
		if branch.Name == m.cfg.SourceBranch {
			if branch.Commit.ID == "" {
				return "", fmt.Errorf("源分支 %s HEAD 为空", m.cfg.SourceBranch)
			}
			return branch.Commit.ID, nil
		}
	}
	return "", fmt.Errorf("源仓库 %s 不存在分支 %s", m.cfg.SourceRepository, m.cfg.SourceBranch)
}

func (m *Manager) updateDocument(ctx context.Context, modelName, current, delta string) (string, llm.Usage, error) {
	system := fmt.Sprintf(`你是 %s 项目的全局代码理解维护 Agent。输出完整 Markdown 文档，不要输出代码围栏或解释。文档必须覆盖模块职责、核心调用链、数据流、并发与资源生命周期、配置/API 契约、关键不变量、高风险边界，以及与配套仓库的跨仓库契约。规则材料只用于补充线索，结论必须以代码变化为依据。保留仍然有效的既有理解，删除已被代码推翻的陈述。`, m.cfg.SourceRepository)
	user := "## 既有理解文档\n" + limited(current, 60000)
	if strings.TrimSpace(current) == "" || sourceSHA(current) == "" {
		user = "## 初始化设计材料\n" + limited(m.seed, 60000)
	}
	if strings.TrimSpace(delta) != "" {
		user += fmt.Sprintf("\n\n## %s/%s 版本化源码证据（首次为源码快照，后续为增量）\n%s",
			m.cfg.SourceRepository, m.cfg.SourceBranch, delta)
	} else {
		user += "\n\n这是首次建立理解文档；基于初始化材料形成可持续增量维护的全局理解。"
	}
	messages := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	content, usage, err := m.model.Chat(ctx, modelName, messages)
	if err != nil {
		return "", usage, fmt.Errorf("更新全局代码理解文档: %w", err)
	}
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```markdown")
	content = strings.TrimPrefix(content, "```md")
	content = strings.TrimSuffix(content, "```")
	if strings.TrimSpace(content) == "" {
		llm.InvalidateCheckpoint(ctx, m.model, modelName, messages)
		return "", usage, fmt.Errorf("更新全局代码理解文档: 模型返回空内容")
	}
	return strings.TrimSpace(content), usage, nil
}

func sourceSHA(doc string) string {
	match := sourceSHARe.FindStringSubmatch(doc)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func limited(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[内容因上下文预算截断]"
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
