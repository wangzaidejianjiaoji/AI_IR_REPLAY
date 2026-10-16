package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type checkpointKey struct{}
type checkpointScope struct{ directory, identity string }
type quotaDirectoryKey struct{}

// WithQuotaDirectory shares provider cooldowns independently of response
// checkpoint scopes. It also works when response checkpoints are disabled.
func WithQuotaDirectory(ctx context.Context, directory string) context.Context {
	return context.WithValue(ctx, quotaDirectoryKey{}, strings.TrimSpace(directory))
}

// WithCheckpoint enables response reuse for one immutable review snapshot.
// The caller must include repository, source SHA and target SHA in scope.
func WithCheckpoint(ctx context.Context, directory, scope string) context.Context {
	if strings.TrimSpace(directory) == "" || scope == "" {
		return ctx
	}
	return context.WithValue(ctx, checkpointKey{}, checkpointScope{directory, scope})
}

type savedResponse struct {
	Version       int    `json:"version"`
	Key           string `json:"key"`
	Content       string `json:"content"`
	ContentHash   string `json:"content_hash"`
	OriginalUsage Usage  `json:"original_usage"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
}

func digest(value []byte) string {
	h := sha256.Sum256(value)
	return hex.EncodeToString(h[:])
}

func (c *Client) checkpointPath(ctx context.Context, model string, messages []Message) (string, string) {
	scope, enabled := ctx.Value(checkpointKey{}).(checkpointScope)
	if !enabled {
		return "", ""
	}
	// Include generation settings and both providers. No credentials or prompts
	// are persisted: they participate only in the request digest.
	fallback := ""
	if c.cfg.Fallback != nil && c.cfg.Fallback.Enabled {
		encoded, _ := json.Marshal(struct {
			URL     string
			Models  map[string]string
			Account string
		}{c.cfg.Fallback.BaseURL, c.cfg.Fallback.Models, digest([]byte(c.cfg.Fallback.APIKey))})
		fallback = digest(encoded)
	}
	encoded, _ := json.Marshal(struct {
		Version                                 int
		Provider, URL, Account, Model, Fallback string
		MaxTokens                               int
		Temperature                             float64
		Messages                                []Message
	}{1, c.cfg.Provider, c.cfg.BaseURL, digest([]byte(c.cfg.APIKey)), model, fallback, c.cfg.MaxTokens, c.cfg.Temperature, messages})
	key := digest(encoded)
	return filepath.Join(scope.directory, "responses", digest([]byte(scope.identity)), key+".json"), key
}

func readResponse(path, key string) (savedResponse, bool) {
	var entry savedResponse
	if path == "" {
		return entry, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &entry) != nil || entry.Version != 1 || entry.Key != key || strings.TrimSpace(entry.Content) == "" || entry.ContentHash != digest([]byte(entry.Content)) {
		return savedResponse{}, false
	}
	return entry, true
}

func writeCheckpoint(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// InvalidateCheckpoint discards a response rejected by a consumer's parser or
// semantic protocol checks. Lightweight model interfaces need not implement it.
func InvalidateCheckpoint(ctx context.Context, client any, model string, messages []Message) {
	if invalidator, ok := client.(interface {
		InvalidateCheckpoint(context.Context, string, []Message)
	}); ok {
		invalidator.InvalidateCheckpoint(ctx, model, messages)
	}
}

func (c *Client) InvalidateCheckpoint(ctx context.Context, model string, messages []Message) {
	path, _ := c.checkpointPath(ctx, model, messages)
	if path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			c.logger.Warn("删除无效模型检查点失败", "error", err)
		}
	}
}

type savedQuota struct {
	Version  int        `json:"version"`
	Identity string     `json:"identity"`
	Quota    QuotaError `json:"quota"`
}

func (c *Client) quotaPath(ctx context.Context) (string, string) {
	scope, ok := ctx.Value(checkpointKey{}).(checkpointScope)
	directory, _ := ctx.Value(quotaDirectoryKey{}).(string)
	if directory == "" && ok {
		directory = scope.directory
	}
	if directory == "" {
		return "", ""
	}
	encoded, _ := json.Marshal([]string{c.cfg.Provider, c.cfg.BaseURL, digest([]byte(c.cfg.APIKey))})
	identity := digest(encoded)
	return filepath.Join(directory, "quotas", identity+".json"), identity
}

func (c *Client) blockedQuota(ctx context.Context) *QuotaError {
	now := time.Now()
	if blocked := c.quota.blocked(now); blocked != nil {
		return blocked
	}
	path, identity := c.quotaPath(ctx)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	var saved savedQuota
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Version != 1 || saved.Identity != identity || !now.Before(saved.Quota.RetryAt) {
		return nil
	}
	c.quota.remember(&saved.Quota)
	return c.quota.blocked(now)
}

func (c *Client) rememberQuota(ctx context.Context, quota *QuotaError) error {
	// Keep the state merge and disk write in one critical section; otherwise a
	// slower writer can persist a shorter deadline after the longer response.
	c.quota.mu.Lock()
	defer c.quota.mu.Unlock()
	c.quota.rememberLocked(quota)
	path, identity := c.quotaPath(ctx)
	if path != "" {
		if err := writeCheckpoint(path, savedQuota{Version: 1, Identity: identity, Quota: *c.quota.err}); err != nil {
			return err
		}
	}
	return nil
}
