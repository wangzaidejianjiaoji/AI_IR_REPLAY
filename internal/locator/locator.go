// Package locator 实现评论定位器（确定性工程层）：
// 将 Agent 返回的问题定位到准确的文件和行号，校验行号是否落在 PR diff 的
// 变更范围内；行号偏移时尝试在 diff hunk 上下文中重新定位，无法定位时
// 降级为文件级评论；文件不在 diff 中的问题直接丢弃（过滤幻觉）。
package locator

import (
	"log/slog"
	"strings"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/diff"
)

// Locator 评论定位器。
type Locator struct {
	verifyLine bool // 是否校验行号（对应配置 comment_location.verify_line_number）
	logger     *slog.Logger
}

// New 创建评论定位器。
func New(verifyLine bool, logger *slog.Logger) *Locator {
	return &Locator{verifyLine: verifyLine, logger: logger}
}

// Locate 对问题列表执行定位校验，返回保留的问题与丢弃数量。
func (l *Locator) Locate(issues []agent.Issue, files []*diff.FileDiff) (kept []agent.Issue, dropped int) {
	index := map[string]*diff.FileDiff{}
	for _, f := range files {
		index[f.Path] = f
	}

	for _, is := range issues {
		fd, ok := index[normalizePath(is.File)]
		if !ok {
			// 容错：LLM 可能只输出文件名或省略部分前缀，按后缀模糊匹配一次
			fd = matchBySuffix(files, is.File)
		}
		if fd == nil {
			dropped++
			l.logger.Debug("定位器丢弃问题：文件不在 diff 中",
				"file", is.File, "agent", is.AgentRole)
			continue
		}
		is.File = fd.Path

		if l.verifyLine && is.Line > 0 && !fd.ContainsLine(is.Line) {
			// 行号偏移：尝试重新定位到最近的 hunk；失败则降级为文件级评论
			if relocated := nearestHunkLine(fd, is.Line); relocated > 0 {
				l.logger.Debug("定位器重定位行号",
					"file", is.File, "from", is.Line, "to", relocated)
				is.Line = relocated
			} else {
				is.Line = 0
			}
		}
		kept = append(kept, is)
	}
	return kept, dropped
}

// normalizePath 去除 LLM 输出中常见的路径前缀噪音。
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}

// matchBySuffix 按路径后缀模糊匹配 diff 文件（仅在唯一命中时返回）。
func matchBySuffix(files []*diff.FileDiff, path string) *diff.FileDiff {
	path = normalizePath(path)
	if path == "" {
		return nil
	}
	var hit *diff.FileDiff
	for _, f := range files {
		if strings.HasSuffix(f.Path, path) || strings.HasSuffix(path, f.Path) {
			if hit != nil {
				return nil // 多个命中，无法唯一定位
			}
			hit = f
		}
	}
	return hit
}

// nearestHunkLine 行号不在变更范围内时，若与某 hunk 边界距离 <= 3 行，
// 吸附到该 hunk 的最近边界；否则返回 0（降级为文件级评论）。
func nearestHunkLine(fd *diff.FileDiff, line int) int {
	const tolerance = 3
	for _, h := range fd.Hunks {
		if line >= h.Start-tolerance && line < h.Start {
			return h.Start
		}
		if line > h.End && line <= h.End+tolerance {
			return h.End
		}
	}
	return 0
}
