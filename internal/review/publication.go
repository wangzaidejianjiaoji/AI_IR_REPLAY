package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type publicationReceipt struct {
	Identity, ReportHash string
	PublishedAt          time.Time
}

// The receipt closes the common "comment succeeded, baseline failed" retry
// window. Platform success and local receipt persistence are still not atomic.
func (s *Service) publishOnce(ctx context.Context, identity, body string, post func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.Review.RuntimeDir, "published", fingerprint(identity)+".json")
	raw, err := os.ReadFile(path)
	if err == nil {
		var receipt publicationReceipt
		if json.Unmarshal(raw, &receipt) != nil || receipt.Identity != identity || receipt.PublishedAt.IsZero() || receipt.ReportHash == "" {
			return fmt.Errorf("发布回执损坏；请核对平台评论后处理本地记录")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := post(); err != nil {
		return err
	}
	if err := writeRuntime(path, publicationReceipt{identity, fingerprint(body), time.Now()}); err != nil {
		return fmt.Errorf("评论已发布但回执保存失败，重试前须核对平台: %w", err)
	}
	return nil
}
