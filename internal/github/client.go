// Package github 使用原生 net/http 封装 GitHub REST API v3：
// PR 列表、diff 获取、评论列表与发布（无第三方依赖，便于交叉编译）。
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client GitHub API 客户端。
type Client struct {
	apiBase string
	token   string
	http    *http.Client
	perPage int
}

// PullRequest PR 元信息（仅保留审查所需字段）。
type PullRequest struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	Body           string  `json:"body"`
	State          string  `json:"state"`
	Mergeable      *bool   `json:"mergeable"`       // nil=计算中, true=可合并, false=有冲突
	MergeableState string  `json:"mergeable_state"` // clean=无冲突, dirty=有冲突, unknown=计算中
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
}

// HasConflict 判断 PR 是否存在合并冲突。
// GitHub 的 mergeable 字段是异步计算的，新 PR 可能为 nil；
// 此时结合 mergeable_state（"dirty" 表示有冲突）做兜底判断。
func (pr *PullRequest) HasConflict() bool {
	if pr.Mergeable != nil {
		return !*pr.Mergeable
	}
	return pr.MergeableState == "dirty"
}

// IsWIP 判断 PR 是否为开发中（Work In Progress）。
// 标题以 [WIP]、WIP:、[wip]、wip: 等前缀开头时视为开发中。
func (pr *PullRequest) IsWIP() bool {
	t := strings.TrimSpace(pr.Title)
	lower := strings.ToLower(t)
	return strings.HasPrefix(lower, "[wip]") || strings.HasPrefix(lower, "wip:")
}

// Reviewers 返回 Assignee + Requested Reviewer 的 login 列表（去重）。
func (pr *PullRequest) Reviewers() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range pr.Assignees {
		if !seen[a.Login] {
			seen[a.Login] = true
			out = append(out, a.Login)
		}
	}
	for _, r := range pr.RequestedReviewers {
		if !seen[r.Login] {
			seen[r.Login] = true
			out = append(out, r.Login)
		}
	}
	return out
}

// IssueComment PR 会话评论。
type IssueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// New 创建 GitHub 客户端。
func New(apiBase, token string, perPage int) *Client {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	if perPage <= 0 {
		perPage = 30
	}
	return &Client{
		apiBase: apiBase,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
		perPage: perPage,
	}
}

// ListOpenPRs 获取仓库的 Open PR 列表（第一页，按更新时间倒序）。
func (c *Client) ListOpenPRs(ctx context.Context, owner, repo string) ([]*PullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&sort=updated&direction=desc&per_page=%d",
		c.apiBase, owner, repo, c.perPage)
	var prs []*PullRequest
	if err := c.getJSON(ctx, url, &prs); err != nil {
		return nil, fmt.Errorf("list PRs %s/%s: %w", owner, repo, err)
	}
	return prs, nil
}

// GetPRDiff 获取 PR 的 unified diff 文本（Accept: diff 媒体类型）。
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, owner, repo, number)
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("get PR diff: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode == 422 {
		// 422 通常表示 PR 存在合并冲突，GitHub 无法生成 diff
		return "", fmt.Errorf("PR #%d 存在合并冲突，无法生成 diff: %s", number, truncate(string(body), 300))
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get PR diff status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return string(body), nil
}

// ListComments 获取 PR 的会话评论列表（用于 Commit SHA 去重检查）。
func (c *Client) ListComments(ctx context.Context, owner, repo string, number int) ([]*IssueComment, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100",
		c.apiBase, owner, repo, number)
	var comments []*IssueComment
	if err := c.getJSON(ctx, url, &comments); err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	return comments, nil
}

// PostComment 在 PR 下发布会话评论。
func (c *Client) PostComment(ctx context.Context, owner, repo string, number int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.apiBase, owner, repo, number)
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post comment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("post comment status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gvs-pr-agent")
	return req, nil
}

func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

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
