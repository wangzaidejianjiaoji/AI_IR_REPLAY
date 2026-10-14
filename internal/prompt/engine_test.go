package prompt

import (
	"strings"
	"testing"
)

func TestUserIncludesGlobalChangeContextBeforeDiff(t *testing.T) {
	got := User("lzvsg/gvs", "更新校验", "", "api", "diff-body", "变更意图：在入口拒绝无效请求")
	contextAt := strings.Index(got, "## 全局代码理解与本次变更意图")
	diffAt := strings.Index(got, "## Diff 内容")
	if contextAt < 0 || diffAt < 0 || contextAt > diffAt {
		t.Fatalf("global change context must appear before diff:\n%s", got)
	}
	if !strings.Contains(got, "入口拒绝无效请求") {
		t.Fatalf("global change context missing:\n%s", got)
	}
}
