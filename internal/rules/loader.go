// Package rules 加载 YAML 审查规则文档，实现四层规则优先级链与热更新：
//
//	优先级 1: CLI 参数 -rule 指定（最高）
//	优先级 2: 项目配置 rules.project_path
//	优先级 3: 全局配置 rules.global_path（默认 ~/.ai-review/review-rules.yaml）
//	优先级 4: 系统默认内嵌规则（最低，兜底）
//
// 每层采用首次命中原则：从高到低取第一个存在且解析成功的规则文档。
// 热更新：Store 定期轮询文件修改时间，变化时重新加载（与 GVS site.yml 热更新一致）。
package rules

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"gvs-pr-agent/internal/diff"
	"gvs-pr-agent/internal/filter"
)

// Document 审查规则文档结构，对应 review-rules-gvs.yaml。
type Document struct {
	Version string              `yaml:"version"`
	Global  []string            `yaml:"global"`
	Agents  map[string][]string `yaml:"agents"`
	Paths   []PathRule          `yaml:"paths"`
}

// PathRule 路径级规则：diff 中包含匹配路径时追加对应规则。
type PathRule struct {
	Pattern string   `yaml:"pattern"`
	Rules   []string `yaml:"rules"`
}

// ForAgent 返回指定 Agent 角色的规则集合：全局规则 + 角色专项规则 + 命中的路径规则。
func (d *Document) ForAgent(role string, files []*diff.FileDiff) []string {
	out := make([]string, 0, len(d.Global)+8)
	out = append(out, d.Global...)
	out = append(out, d.Agents[role]...)
	for _, pr := range d.Paths {
		for _, f := range files {
			if filter.MatchPath(f.Path, pr.Pattern) {
				out = append(out, pr.Rules...)
				break
			}
		}
	}
	return out
}

// Store 规则文档存储：四层优先级链解析 + 线程安全的热更新。
type Store struct {
	mu       sync.RWMutex
	doc      *Document
	source   string    // 当前生效的规则来源（层级描述）
	path     string    // 当前生效的规则文件路径（内嵌规则时为空）
	modTime  time.Time // 当前文件的修改时间（用于热更新检测）
	cliPath  string    // 优先级 1: CLI -rule
	projPath string    // 优先级 2: 项目配置
	globPath string    // 优先级 3: 全局配置
	logger   *slog.Logger
}

// NewStore 按四层优先级链解析并加载规则文档。
// cliPath / projectPath / globalPath 依次对应优先级 1/2/3，均可为空。
func NewStore(cliPath, projectPath, globalPath string, logger *slog.Logger) (*Store, error) {
	if globalPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			globalPath = filepath.Join(home, ".ai-review", "review-rules.yaml")
		}
	}
	s := &Store{
		cliPath:  cliPath,
		projPath: projectPath,
		globPath: expandHome(globalPath),
		logger:   logger,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Current 返回当前生效的规则文档（读多写少，RWMutex 保护）。
func (s *Store) Current() *Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc
}

// Source 返回当前生效规则的来源描述（用于日志）。
func (s *Store) Source() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.source
}

// load 按优先级链从高到低取第一个可用的规则文档。
func (s *Store) load() error {
	layers := []struct {
		name string
		path string
	}{
		{"CLI 参数 -rule", s.cliPath},
		{"项目配置", s.projPath},
		{"全局配置", s.globPath},
	}

	for _, layer := range layers {
		if layer.path == "" {
			continue
		}
		doc, mod, err := parseFile(layer.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // 该层文件不存在，降级到下一层
			}
			// CLI 显式指定的文件解析失败视为致命错误；其余层降级
			if layer.name == "CLI 参数 -rule" {
				return fmt.Errorf("加载规则文档 %s: %w", layer.path, err)
			}
			s.logger.Warn("规则文档解析失败，降级到下一层",
				"layer", layer.name, "path", layer.path, "error", err.Error())
			continue
		}
		s.mu.Lock()
		s.doc, s.source, s.path, s.modTime = doc, layer.name, layer.path, mod
		s.mu.Unlock()
		return nil
	}

	// 优先级 4: 系统默认内嵌规则
	s.mu.Lock()
	s.doc, s.source, s.path = defaultDocument(), "系统默认内嵌规则", ""
	s.mu.Unlock()
	return nil
}

// StartHotReload 启动热更新轮询 goroutine：按 interval 检查文件修改时间，
// 变化时重新执行四层链加载。stop 关闭时退出。
func (s *Store) StartHotReload(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.reloadIfChanged()
			}
		}
	}()
	s.logger.Info("规则热更新已启用", "interval", interval.String(), "source", s.Source())
}

// reloadIfChanged 检查当前生效文件（及更高优先级层）是否变化，变化时重载。
func (s *Store) reloadIfChanged() {
	s.mu.RLock()
	path, modTime := s.path, s.modTime
	s.mu.RUnlock()

	changed := false
	if path == "" {
		// 当前使用内嵌规则：检查是否有更高优先级文件新出现
		for _, p := range []string{s.cliPath, s.projPath, s.globPath} {
			if p != "" {
				if _, err := os.Stat(p); err == nil {
					changed = true
					break
				}
			}
		}
	} else if st, err := os.Stat(path); err != nil || !st.ModTime().Equal(modTime) {
		changed = true
	}
	if !changed {
		return
	}

	if err := s.load(); err != nil {
		s.logger.Error("规则热更新失败，保留旧规则", "error", err.Error())
		return
	}
	s.logger.Info("规则文档已热更新", "source", s.Source(), "version", s.Current().Version)
}

// parseFile 解析单个规则文档文件，返回文档与文件修改时间。
func parseFile(path string) (*Document, time.Time, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	doc := &Document{}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, time.Time{}, fmt.Errorf("parse yaml: %w", err)
	}
	return doc, st.ModTime(), nil
}

// expandHome 展开路径中的 ~ 前缀。
func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// defaultDocument 系统默认内嵌规则（四层优先级链的最低层兜底）。
func defaultDocument() *Document {
	return &Document{
		Version: "1.0",
		Global: []string{
			"只审查本次 diff 中的变更代码，不要评论未变更的代码",
			"问题必须基于 diff 中实际存在的代码，禁止臆测（避免幻觉）",
			"评论使用中文，建议要具体可操作",
		},
	}
}
