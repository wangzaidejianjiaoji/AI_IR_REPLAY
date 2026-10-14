// Package diff 解析 unified diff 格式的 PR 变更内容，
// 提取变更文件列表、变更行数和 diff hunk 行号范围（供评论定位器校验）。
package diff

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

// FileDiff 单个文件的 diff 信息。
type FileDiff struct {
	Path      string      // 变更后文件路径
	OldPath   string      // 变更前文件路径（重命名时不同）
	Added     int         // 新增行数
	Deleted   int         // 删除行数
	Hunks     []HunkRange // 变更行号范围（基于新文件）
	Patch     string      // 该文件的完整 diff 片段
	IsBinary  bool        // 是否二进制文件
	IsDeleted bool        // 是否被删除的文件
	IsRenamed bool        // 是否重命名
}

// HunkRange 一个 diff hunk 在新文件中覆盖的行号范围。
type HunkRange struct {
	Start int // 起始行号（新文件）
	End   int // 结束行号（新文件）
}

// ContainsLine 判断行号是否落在该文件的任一 hunk 范围内（评论定位校验）。
func (f *FileDiff) ContainsLine(line int) bool {
	for _, h := range f.Hunks {
		if line >= h.Start && line <= h.End {
			return true
		}
	}
	return false
}

var (
	diffHeaderRe = regexp.MustCompile(`^diff --git a/(.+?) b/(.+)$`)
	hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
)

// Parse 解析完整的 unified diff 文本为文件级 diff 列表。
func Parse(raw string) []*FileDiff {
	var files []*FileDiff
	var cur *FileDiff
	var patch strings.Builder

	flush := func() {
		if cur != nil {
			cur.Patch = patch.String()
			files = append(files, cur)
		}
		patch.Reset()
	}

	scanner := bufio.NewScanner(strings.NewReader(raw))
	// The complete input is already in memory. Do not silently stop at a long
	// source line: review admission/text budgets decide whether it is accepted.
	scanner.Buffer(make([]byte, 1024*1024), max(1024*1024, len(raw)+1))
	for scanner.Scan() {
		line := scanner.Text()

		if m := diffHeaderRe.FindStringSubmatch(line); m != nil {
			flush()
			cur = &FileDiff{OldPath: m[1], Path: m[2], IsRenamed: m[1] != m[2]}
			patch.WriteString(line + "\n")
			continue
		}
		if cur == nil {
			continue
		}
		patch.WriteString(line + "\n")

		switch {
		case strings.HasPrefix(line, "Binary files"):
			cur.IsBinary = true
		case strings.HasPrefix(line, "deleted file"):
			cur.IsDeleted = true
		case strings.HasPrefix(line, "@@"):
			if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
				start, _ := strconv.Atoi(m[1])
				count := 1
				if m[2] != "" {
					count, _ = strconv.Atoi(m[2])
				}
				end := start
				if count > 0 {
					end = start + count - 1
				}
				cur.Hunks = append(cur.Hunks, HunkRange{Start: start, End: end})
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			cur.Added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			cur.Deleted++
		}
	}
	flush()
	return files
}

// TotalChangedLines 统计所有文件的变更总行数（用于模型分级选择）。
func TotalChangedLines(files []*FileDiff) int {
	total := 0
	for _, f := range files {
		total += f.Added + f.Deleted
	}
	return total
}
