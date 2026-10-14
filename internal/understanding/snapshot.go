package understanding

import (
	"context"
	"crypto/sha256"
	"fmt"
	"gvs-pr-agent/internal/config"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// gitSnapshot reads committed blobs, never mutable checkout contents or symlinks.
// The selected source scope and content hashes travel with the model evidence.
func gitSnapshot(ctx context.Context, cfg config.UnderstandingConfig, sha string) (string, error) {
	if cfg.SnapshotRepository == "" {
		return "", fmt.Errorf("understanding.snapshot_repository 未配置")
	}
	git := func(args ...string) ([]byte, error) {
		b, err := exec.CommandContext(ctx, "git", append([]string{"-C", cfg.SnapshotRepository}, args...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("读取本地 Git 对象失败: %w", err)
		}
		return b, nil
	}
	resolved, err := git("rev-parse", "--verify", "--end-of-options", sha+"^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(resolved))
	if commit != sha {
		return "", fmt.Errorf("源码快照必须匹配完整提交 SHA")
	}
	tree, err := git("ls-tree", "-r", "-z", commit)
	if err != nil {
		return "", err
	}
	limit := cfg.MaxSnapshotBytes
	if limit == 0 {
		limit = 1000000
	}
	var result strings.Builder
	fmt.Fprintf(&result, "SOURCE-SNAPSHOT-SHA: %s\n范围：Git 中的文本源码/协议/构建与配置，排除 vendor/third_party、符号链接及二进制。\n", commit)
	count := 0
	for _, entry := range strings.Split(string(tree), "\x00") {
		parts := strings.SplitN(entry, "\t", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "100") {
			continue
		}
		name := parts[1]
		if strings.Contains("/"+name, "/vendor/") || strings.Contains("/"+name, "/third_party/") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		selected := name == "CMakeLists.txt" || strings.HasSuffix(name, "/CMakeLists.txt")
		switch ext {
		case ".go", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".proto", ".yaml", ".yml", ".mod", ".cmake", ".sh":
			selected = true
		}
		if !selected {
			continue
		}
		body, err := git("show", commit+":"+name)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(body) || strings.ContainsRune(string(body), 0) {
			continue
		}
		if result.Len()+len(body)+len(name)+100 > limit {
			return "", fmt.Errorf("源码快照超过 max_snapshot_bytes=%d，禁止静默截断", limit)
		}
		fmt.Fprintf(&result, "\nFILE: %s SHA256: %x\n%s\n", name, sha256.Sum256(body), body)
		count++
	}
	if count == 0 {
		return "", fmt.Errorf("源码快照未包含可审查文本文件")
	}
	fmt.Fprintf(&result, "\nSOURCE-FILE-COUNT: %d\n", count)
	return result.String(), nil
}
