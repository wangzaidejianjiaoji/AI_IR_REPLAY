package packager

import (
	"fmt"
	"strings"

	"gvs-pr-agent/internal/diff"
)

// CompleteHunks returns each complete hunk with the original file header.
// Metadata-only and binary patches remain indivisible. The input is unchanged.
func CompleteHunks(file *diff.FileDiff) []*diff.FileDiff {
	var starts []int
	offset := 0
	for _, line := range strings.SplitAfter(file.Patch, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			starts = append(starts, offset)
		}
		offset += len(line)
	}
	if len(starts) == 0 {
		return []*diff.FileDiff{file}
	}
	header := file.Patch[:starts[0]]
	var out []*diff.FileDiff
	for i, start := range starts {
		end := len(file.Patch)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		patch := header + file.Patch[start:end]
		parsed := diff.Parse(patch)
		if len(parsed) != 1 || len(parsed[0].Hunks) != 1 {
			return []*diff.FileDiff{file}
		}
		part := *file
		part.Patch = patch
		part.Hunks = parsed[0].Hunks
		part.Added = parsed[0].Added
		part.Deleted = parsed[0].Deleted
		out = append(out, &part)
	}
	return out
}

func splitBounded(file *diff.FileDiff, maxBytes int) ([]*diff.FileDiff, error) {
	if maxBytes <= 0 || len(file.Patch) <= maxBytes {
		return []*diff.FileDiff{file}, nil
	}
	parts := CompleteHunks(file)
	for _, part := range parts {
		if len(part.Patch) > maxBytes {
			return nil, fmt.Errorf("审查覆盖不完整：文件 %s 的完整 hunk 为 %d 字节，超过 max_diff_chars=%d；拒绝截断审核", file.Path, len(part.Patch), maxBytes)
		}
	}
	return parts, nil
}
