// Package codeup 使用原生 net/http 封装阿里云 Codeup OpenAPI（中心版）：
// 分支列表、代码比较（compare diff）、commit 评论、合并请求列表/版本/评论
// （无第三方依赖，与 github 包风格一致）。
//
// 认证方式：请求头 x-yunxiao-token 携带个人访问令牌。
// 接口文档：https://help.aliyun.com/zh/yunxiao/developer-reference/
package codeup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client Codeup OpenAPI 客户端。
type Client struct {
	baseURL string // API 前缀，如 https://openapi-rdc.aliyuncs.com（测试时可指向 httptest）
	orgID   string // 组织 ID（中心版必填）
	token   string // 个人访问令牌
	http    *http.Client
	perPage int
}

// Branch 分支元信息（仅保留审查所需字段）。
type Branch struct {
	Name          string `json:"name"`
	DefaultBranch bool   `json:"defaultBranch"`
	Protected     bool   `json:"protected"`
	Commit        struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		AuthorName    string `json:"authorName"`
		CommittedDate string `json:"committedDate"`
	} `json:"commit"`
}

// CommitInfo compare 结果中的差异提交信息。
type CommitInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// compareResult GetCompare 接口返回结构。
type compareResult struct {
	Commits []CommitInfo `json:"commits"`
	Diffs   []struct {
		Diff        string `json:"diff"`
		OldPath     string `json:"oldPath"`
		NewPath     string `json:"newPath"`
		AMode       string `json:"aMode"`
		BMode       string `json:"bMode"`
		NewFile     bool   `json:"newFile"`
		DeletedFile bool   `json:"deletedFile"`
		RenamedFile bool   `json:"renamedFile"`
		IsBinary    bool   `json:"isBinary"`
	} `json:"diffs"`
}

// New 创建 Codeup 客户端。domain 支持裸域名或带 http(s):// 前缀（测试注入）。
func New(domain, orgID, token string, perPage int) *Client {
	if domain == "" {
		domain = "openapi-rdc.aliyuncs.com"
	}
	if !strings.HasPrefix(domain, "http://") && !strings.HasPrefix(domain, "https://") {
		domain = "https://" + domain
	}
	if perPage <= 0 || perPage > 100 {
		perPage = 100
	}
	return &Client{
		baseURL: strings.TrimSuffix(domain, "/"),
		orgID:   orgID,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
		perPage: perPage,
	}
}

// orgBase 返回组织级 API 前缀。
func (c *Client) orgBase() string {
	return fmt.Sprintf("%s/oapi/v1/codeup/organizations/%s", c.baseURL, c.orgID)
}

// repoBase 返回单仓库 API 前缀；repoID 可为数字 ID 或仓库全路径（自动 URL 编码）。
func (c *Client) repoBase(repoID string) string {
	return fmt.Sprintf("%s/repositories/%s", c.orgBase(), url.PathEscape(repoID))
}

// ListBranches 获取仓库全部分支（按更新时间倒序，自动翻页，安全上限 20 页）。
func (c *Client) ListBranches(ctx context.Context, repoID string) ([]*Branch, error) {
	var all []*Branch
	for page := 1; page <= 20; page++ {
		u := fmt.Sprintf("%s/branches?page=%d&perPage=%d&sort=updated_desc",
			c.repoBase(repoID), page, c.perPage)
		var branches []*Branch
		if err := c.getJSON(ctx, u, &branches); err != nil {
			return nil, fmt.Errorf("list branches %s: %w", repoID, err)
		}
		all = append(all, branches...)
		if len(branches) < c.perPage {
			break
		}
	}
	return all, nil
}

// GetCompareDiff 获取 from..to 的代码比较结果，将逐文件 diff 片段拼装为
// 标准 unified diff 文本（补全 diff --git 头，供 diff.Parse 解析）。
// from/to 可为 CommitSHA、分支名或标签名；默认使用 Merge-Base 比较。
func (c *Client) GetCompareDiff(ctx context.Context, repoID, from, to string) (string, []CommitInfo, error) {
	return c.getCompareDiff(ctx, repoID, from, to, false)
}

// GetVersionDiff compares the exact trees of two reviewed source versions.
// Unlike merge-base comparison, this includes reversions after rebase/force-push.
func (c *Client) GetVersionDiff(ctx context.Context, repoID, from, to string) (string, []CommitInfo, error) {
	return c.getCompareDiff(ctx, repoID, from, to, true)
}

func (c *Client) getCompareDiff(ctx context.Context, repoID, from, to string, straight bool) (string, []CommitInfo, error) {
	u := fmt.Sprintf("%s/compares?from=%s&to=%s",
		c.repoBase(repoID), url.QueryEscape(from), url.QueryEscape(to))
	if straight {
		u += "&straight=true"
	}
	var res compareResult
	if err := c.getJSON(ctx, u, &res); err != nil {
		return "", nil, fmt.Errorf("get compare %s %s..%s: %w", repoID, short(from), short(to), err)
	}

	var sb strings.Builder
	for _, d := range res.Diffs {
		oldPath, newPath := d.OldPath, d.NewPath
		if oldPath == "" {
			oldPath = newPath
		}
		if newPath == "" {
			newPath = oldPath
		}
		fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", oldPath, newPath)
		if d.NewFile {
			fmt.Fprintf(&sb, "new file mode %s\n", d.BMode)
		}
		if d.DeletedFile {
			fmt.Fprintf(&sb, "deleted file mode %s\n", d.AMode)
		}
		if d.RenamedFile {
			fmt.Fprintf(&sb, "rename from %s\nrename to %s\n", oldPath, newPath)
		}
		if d.IsBinary {
			fmt.Fprintf(&sb, "Binary files a/%s and b/%s differ\n", oldPath, newPath)
			continue
		}
		sb.WriteString(d.Diff)
		if !strings.HasSuffix(d.Diff, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String(), res.Commits, nil
}

// CreateCommitComment 给单个提交添加评论。
func (c *Client) CreateCommitComment(ctx context.Context, repoID, sha, content string) error {
	u := fmt.Sprintf("%s/commits/%s/comments", c.repoBase(repoID), url.PathEscape(sha))
	return c.postJSON(ctx, u, map[string]any{"content": content}, "create commit comment")
}

// ChangeRequest 合并请求元信息（新版 MR，仅保留审查所需字段）。
type ChangeRequest struct {
	LocalID      int    `json:"localId"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	State        string `json:"state"` // UNDER_DEV/UNDER_REVIEW/TO_BE_MERGED/CLOSED/MERGED
	SourceBranch string `json:"sourceBranch"`
	TargetBranch string `json:"targetBranch"`
	ProjectID    int64  `json:"projectId"`
	MergeStatus  string `json:"mergeStatus"` // can_be_merged=可合并, conflict=有冲突, checking=计算中
	Author       struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"author"`
}

// HasConflict 判断 MR 是否存在合并冲突。
// Codeup 的 mergeStatus 字段标识合并状态："conflict" 表示存在冲突。
func (mr *ChangeRequest) HasConflict() bool {
	return mr.MergeStatus == "conflict" || mr.MergeStatus == "cannot_be_merged"
}

// IsWIP 判断 MR 是否为开发中（Work In Progress）。
// 标题以 [WIP]、WIP:、[wip]、wip: 等前缀开头时视为开发中。
func (mr *ChangeRequest) IsWIP() bool {
	t := strings.TrimSpace(mr.Title)
	lower := strings.ToLower(t)
	return strings.HasPrefix(lower, "[wip]") || strings.HasPrefix(lower, "wip:")
}

// PatchSet 合并请求版本（评论需关联 patchSetBizId）。
type PatchSet struct {
	PatchSetBizID        string `json:"patchSetBizId"`
	CommitID             string `json:"commitId"`
	VersionNo            int    `json:"versionNo"`
	RelatedMergeItemType string `json:"relatedMergeItemType"` // MERGE_SOURCE / MERGE_TARGET
}

// ListOpenChangeRequests 查询指定仓库的开启状态合并请求列表（按更新时间倒序）。
func (c *Client) ListOpenChangeRequests(ctx context.Context, repoID string) ([]*ChangeRequest, error) {
	u := fmt.Sprintf("%s/changeRequests?projectIds=%s&state=opened&orderBy=updated_at&sort=desc&page=1&perPage=%d",
		c.orgBase(), url.QueryEscape(repoID), c.perPage)
	var mrs []*ChangeRequest
	if err := c.getJSON(ctx, u, &mrs); err != nil {
		return nil, fmt.Errorf("list change requests %s: %w", repoID, err)
	}
	return mrs, nil
}

// ListPatchSets 查询合并请求的版本列表（评论关联与新提交检测用）。
func (c *Client) ListPatchSets(ctx context.Context, repoID string, localID int) ([]*PatchSet, error) {
	u := fmt.Sprintf("%s/changeRequests/%d/diffs/patches", c.repoBase(repoID), localID)
	var ps []*PatchSet
	if err := c.getJSON(ctx, u, &ps); err != nil {
		return nil, fmt.Errorf("list patch sets %s!%d: %w", repoID, localID, err)
	}
	return ps, nil
}

// SourcePatchSet 返回版本列表中版本号最大的合并源版本（当前待审查版本）。
func SourcePatchSet(ps []*PatchSet) *PatchSet {
	var out *PatchSet
	for _, p := range ps {
		if p.RelatedMergeItemType != "MERGE_SOURCE" {
			continue
		}
		if out == nil || p.VersionNo > out.VersionNo {
			out = p
		}
	}
	return out
}

// CreateChangeRequestComment 在合并请求下发布全局评论（关联当前合并源版本）。
func (c *Client) CreateChangeRequestComment(ctx context.Context, repoID string, localID int,
	patchSetBizID, content string) error {
	u := fmt.Sprintf("%s/changeRequests/%d/comments", c.repoBase(repoID), localID)
	payload := map[string]any{
		"comment_type":    "GLOBAL_COMMENT",
		"content":         content,
		"draft":           false,
		"resolved":        false,
		"patchset_biz_id": patchSetBizID,
	}
	return c.postJSON(ctx, u, payload, "create change request comment")
}

// postJSON 发送 POST 请求，2xx 之外视为失败。
func (c *Client) postJSON(ctx context.Context, u string, body any, action string) error {
	return c.sendJSON(ctx, http.MethodPost, u, body, action)
}

func (c *Client) sendJSON(ctx context.Context, method, u string, body any, action string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, method, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s status %d: %s", action, resp.StatusCode, truncate(string(respBody), 300))
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-yunxiao-token", c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gvs-pr-agent")
	return req, nil
}

func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return json.Unmarshal(body, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
