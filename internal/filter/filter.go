// Package filter 实现文件筛选（include/exclude glob 匹配）与分支过滤。
// glob 支持 ** 跨目录通配，无第三方依赖。
package filter

import (
	"regexp"
	"strings"
	"sync"

	"gvs-pr-agent/internal/diff"
)

// Files 应用 include/exclude 规则筛选变更文件，并剔除二进制/已删除文件。
func Files(files []*diff.FileDiff, include, exclude []string) []*diff.FileDiff {
	var out []*diff.FileDiff
	for _, f := range files {
		if f.IsBinary || f.IsDeleted {
			continue
		}
		if len(include) > 0 && !matchAny(f.Path, include) {
			continue
		}
		if matchAny(f.Path, exclude) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// MatchBranch 判断分支名是否通过 include/exclude 过滤。
func MatchBranch(branch string, include, exclude []string) bool {
	if matchAny(branch, exclude) {
		return false
	}
	if len(include) == 0 {
		return true
	}
	return matchAny(branch, include)
}

// MatchPath 判断单个路径是否匹配指定 glob 模式（供 rules 等包复用）。
func MatchPath(path, pattern string) bool {
	return matchAny(path, []string{pattern})
}

// matchAny 判断路径是否匹配任一 glob 模式。
// 兼容两种习惯写法：前缀式 "gvs/" 与 glob 式 "**/vendor/**"。
func matchAny(path string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		// 前缀式：以 / 结尾表示目录前缀
		if strings.HasSuffix(p, "/") && !strings.Contains(p, "*") {
			if strings.HasPrefix(path, p) {
				return true
			}
			continue
		}
		if globMatch(p, path) {
			return true
		}
	}
	return false
}

// globMatch 将 glob 模式编译为正则进行匹配（** 跨目录，* 单段，? 单字符）。
func globMatch(pattern, path string) bool {
	re, err := compileGlob(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(path)
}

// globCache 编译结果缓存；多 Agent 并发审查时会同时访问，RWMutex 保护。
var (
	globMu    sync.RWMutex
	globCache = map[string]*regexp.Regexp{}
)

func compileGlob(pattern string) (*regexp.Regexp, error) {
	globMu.RLock()
	re, ok := globCache[pattern]
	globMu.RUnlock()
	if ok {
		return re, nil
	}
	var sb strings.Builder
	sb.WriteString("^")
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// "**/" 匹配零个或多个目录段；结尾 "**" 匹配任意后缀
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					sb.WriteString(`(?:[^/]+/)*`)
					i += 3
				} else {
					sb.WriteString(`.*`)
					i += 2
				}
			} else {
				sb.WriteString(`[^/]*`)
				i++
			}
		case '?':
			sb.WriteString(`[^/]`)
			i++
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return nil, err
	}
	globMu.Lock()
	globCache[pattern] = re
	globMu.Unlock()
	return re, nil
}
