// codeup 客户端单元测试：httptest 模拟 Codeup OpenAPI，
// 覆盖分支列表翻页、compare diff 拼装、commit/MR 评论、认证头与错误处理。
package codeup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gvs-pr-agent/internal/diff"
)

// newTestServer 启动模拟 Codeup OpenAPI 的 httptest 服务。
func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "test-org", "test-token", 2)
}

func TestListBranchesPagingAndAuth(t *testing.T) {
	var gotToken string
	pages := map[string][]map[string]any{
		"1": {
			{"name": "feature/a", "commit": map[string]any{"id": "sha-a"}},
			{"name": "feature/b", "commit": map[string]any{"id": "sha-b"}},
		},
		"2": {
			{"name": "master", "defaultBranch": true, "commit": map[string]any{"id": "sha-m"}},
		},
	}
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("x-yunxiao-token")
		wantPath := "/oapi/v1/codeup/organizations/test-org/repositories/lzvsg%2Fgvs/branches"
		if r.URL.EscapedPath() != wantPath {
			t.Errorf("path = %s, want %s", r.URL.EscapedPath(), wantPath)
		}
		_ = json.NewEncoder(w).Encode(pages[r.URL.Query().Get("page")])
	})

	branches, err := c.ListBranches(context.Background(), "lzvsg/gvs")
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "test-token" {
		t.Errorf("x-yunxiao-token = %q, want test-token", gotToken)
	}
	// perPage=2：第 1 页满页继续翻，第 2 页 1 条终止
	if len(branches) != 3 {
		t.Fatalf("branches = %d, want 3", len(branches))
	}
	if branches[0].Name != "feature/a" || branches[0].Commit.ID != "sha-a" {
		t.Errorf("branch[0] = %+v", branches[0])
	}
	if !branches[2].DefaultBranch {
		t.Errorf("branch[2] 应为默认分支")
	}
}

func TestGetCompareDiffAssembly(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("from"); got != "base-sha" {
			t.Errorf("from = %s, want base-sha", got)
		}
		if got := r.URL.Query().Get("to"); got != "head-sha" {
			t.Errorf("to = %s, want head-sha", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"commits": []map[string]any{{"id": "head-sha", "title": "feat: x"}},
			"diffs": []map[string]any{
				{ // 普通修改
					"oldPath": "a.go", "newPath": "a.go",
					"diff": "--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n ctx\n-old\n+new\n+more\n",
				},
				{ // 新增文件
					"oldPath": "", "newPath": "b.go", "newFile": true, "bMode": "100644",
					"diff": "--- /dev/null\n+++ b/b.go\n@@ -0,0 +1 @@\n+pkg\n",
				},
				{ // 删除文件
					"oldPath": "c.go", "newPath": "c.go", "deletedFile": true, "aMode": "100644",
					"diff": "--- a/c.go\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-gone\n",
				},
				{ // 二进制
					"oldPath": "d.png", "newPath": "d.png", "isBinary": true, "diff": "",
				},
			},
		})
	})

	raw, commits, err := c.GetCompareDiff(context.Background(), "123", "base-sha", "head-sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].ID != "head-sha" {
		t.Errorf("commits = %+v", commits)
	}

	// 拼装结果必须能被 diff.Parse 正确解析
	files := diff.Parse(raw)
	if len(files) != 4 {
		t.Fatalf("解析文件数 = %d, want 4; raw:\n%s", len(files), raw)
	}
	if files[0].Path != "a.go" || files[0].Added != 2 || files[0].Deleted != 1 {
		t.Errorf("files[0] = %+v", files[0])
	}
	if files[1].Path != "b.go" || files[1].Added != 1 {
		t.Errorf("files[1] = %+v", files[1])
	}
	if !files[2].IsDeleted {
		t.Errorf("files[2] 应标记为删除文件: %+v", files[2])
	}
	if !files[3].IsBinary {
		t.Errorf("files[3] 应标记为二进制: %+v", files[3])
	}
}

func TestCreateCommitComment(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	})

	if err := c.CreateCommitComment(context.Background(), "123", "abc123", "审查报告"); err != nil {
		t.Fatal(err)
	}
	if want := "/oapi/v1/codeup/organizations/test-org/repositories/123/commits/abc123/comments"; gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
	if gotBody["content"] != "审查报告" {
		t.Errorf("content = %v", gotBody["content"])
	}
}

func TestCreateCommitCommentError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})
	err := c.CreateCommitComment(context.Background(), "123", "abc", "x")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("期望 403 错误, got %v", err)
	}
}

func TestListOpenChangeRequests(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "opened" {
			t.Errorf("state = %s, want opened", got)
		}
		if got := r.URL.Query().Get("projectIds"); got != "lzvsg/gvs" {
			t.Errorf("projectIds = %s", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"localId": 8, "title": "feat: qatool", "state": "UNDER_REVIEW",
			"sourceBranch": "qatool-zhangty", "targetBranch": "master",
			"author": map[string]any{"name": "张腾予", "username": "zhangty"},
		}})
	})

	mrs, err := c.ListOpenChangeRequests(context.Background(), "lzvsg/gvs")
	if err != nil {
		t.Fatal(err)
	}
	if len(mrs) != 1 {
		t.Fatalf("mrs = %d, want 1", len(mrs))
	}
	mr := mrs[0]
	if mr.LocalID != 8 || mr.SourceBranch != "qatool-zhangty" || mr.Author.Name != "张腾予" {
		t.Errorf("mr = %+v", mr)
	}
}

func TestListPatchSetsAndSourcePatchSet(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if want := "/oapi/v1/codeup/organizations/test-org/repositories/123/changeRequests/8/diffs/patches"; r.URL.EscapedPath() != want {
			t.Errorf("path = %s, want %s", r.URL.EscapedPath(), want)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"patchSetBizId": "ps-target", "commitId": "t-sha", "versionNo": 1, "relatedMergeItemType": "MERGE_TARGET"},
			{"patchSetBizId": "ps-v1", "commitId": "s-sha-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"},
			{"patchSetBizId": "ps-v2", "commitId": "s-sha-2", "versionNo": 2, "relatedMergeItemType": "MERGE_SOURCE"},
		})
	})

	ps, err := c.ListPatchSets(context.Background(), "123", 8)
	if err != nil {
		t.Fatal(err)
	}
	src := SourcePatchSet(ps)
	if src == nil || src.PatchSetBizID != "ps-v2" || src.CommitID != "s-sha-2" {
		t.Errorf("SourcePatchSet = %+v, want ps-v2", src)
	}
	if SourcePatchSet(nil) != nil {
		t.Error("空列表应返回 nil")
	}
}

func TestCreateChangeRequestComment(t *testing.T) {
	var gotBody map[string]any
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if want := "/oapi/v1/codeup/organizations/test-org/repositories/123/changeRequests/8/comments"; r.URL.EscapedPath() != want {
			t.Errorf("path = %s, want %s", r.URL.EscapedPath(), want)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{}`)
	})

	if err := c.CreateChangeRequestComment(context.Background(), "123", 8, "ps-v2", "MR 审查报告"); err != nil {
		t.Fatal(err)
	}
	if gotBody["comment_type"] != "GLOBAL_COMMENT" || gotBody["patchset_biz_id"] != "ps-v2" ||
		gotBody["content"] != "MR 审查报告" || gotBody["draft"] != false {
		t.Errorf("body = %+v", gotBody)
	}
}
