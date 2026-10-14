// filter 并发安全回归测试（源自真实演练崩溃）：
// 多 Agent 并发审查时 globCache 曾因无锁 map 触发 concurrent map read and map write。
package filter

import (
	"fmt"
	"sync"
	"testing"
)

func TestGlobMatchConcurrent(t *testing.T) {
	patterns := []string{"**/vendor/**", "*.go", "gvs/", "release/*", "**/*_test.go"}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				path := fmt.Sprintf("gvs/pkg%d/file%d.go", n, j)
				for _, p := range patterns {
					_ = MatchPath(path, p)
				}
				_ = MatchBranch("qatool-zhangty", patterns, nil)
			}
		}(i)
	}
	wg.Wait()
}

func TestMatchBranch(t *testing.T) {
	cases := []struct {
		branch  string
		include []string
		exclude []string
		want    bool
	}{
		{"qatool-zhangty", []string{"qatool-zhangty"}, nil, true},
		{"other", []string{"qatool-zhangty"}, nil, false},
		{"release/hotfix", nil, []string{"release/*"}, false},
		{"feature/x", nil, nil, true}, // include 为空不限制
		{"qatool-abc", []string{"qatool-*"}, nil, true},
	}
	for _, c := range cases {
		if got := MatchBranch(c.branch, c.include, c.exclude); got != c.want {
			t.Errorf("MatchBranch(%q, %v, %v) = %v, want %v",
				c.branch, c.include, c.exclude, got, c.want)
		}
	}
}
