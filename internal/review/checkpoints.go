package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gvs-pr-agent/internal/llm"
	"gvs-pr-agent/internal/rules"
	"gvs-pr-agent/internal/understanding"
)

type checkpointContextKey struct{}
type pinnedRulesKey struct{}
type checkpointContext struct {
	directory, identity string
	rules               *rules.Document
}

func (s *Service) taskSignature() string {
	return s.signature(s.rulesStore.Current())
}

func (s *Service) signature(doc *rules.Document) string {
	return fingerprint(struct {
		Protocol      int
		Rules         any
		Understanding any
		Agents        any
		Review        any
		Model         any
	}{2, doc, s.cfg.Understanding, s.cfg.Agents, s.cfg.Review, s.cfg.LLM})
}

func (s *Service) checkpointContext(ctx context.Context, identity string) context.Context {
	doc := s.reviewRules(ctx)
	// Relevant dependencies participate only as hashes, never raw credentials.
	identity += ":" + s.signature(doc)
	ctx = context.WithValue(ctx, checkpointContextKey{}, checkpointContext{s.cfg.Review.CheckpointDir, identity, doc})
	return llm.WithCheckpoint(ctx, s.cfg.Review.CheckpointDir, identity)
}

func (s *Service) reviewRules(ctx context.Context) *rules.Document {
	if doc, ok := ctx.Value(pinnedRulesKey{}).(*rules.Document); ok {
		return doc
	}
	if value, ok := ctx.Value(checkpointContextKey{}).(checkpointContext); ok {
		return value.rules
	}
	return s.rulesStore.Current()
}

type savedKnowledge struct {
	Snapshot understanding.Snapshot
	Hash     string
	Degraded bool
}

type snapshotPersistenceError struct{ error }

// Pin successful knowledge for this review so resumed analysis does not change
// prompts simply because main has advanced meanwhile.
func (s *Service) prepareKnowledge(ctx context.Context, model string) (understanding.Snapshot, llm.Usage, error) {
	path := ""
	if scope, ok := ctx.Value(checkpointContextKey{}).(checkpointContext); ok && scope.directory != "" {
		path = filepath.Join(scope.directory, "knowledge", fingerprint(scope.identity)+".json")
		raw, err := os.ReadFile(path)
		if err == nil {
			var saved savedKnowledge
			if json.Unmarshal(raw, &saved) == nil && saved.Hash == fingerprint(saved.Snapshot) {
				if saved.Degraded {
					return saved.Snapshot, llm.Usage{}, fmt.Errorf("恢复此前审核使用的降级知识快照")
				}
				return saved.Snapshot, llm.Usage{}, nil
			}
		} else if !os.IsNotExist(err) {
			return understanding.Snapshot{}, llm.Usage{}, snapshotPersistenceError{err}
		}
	}
	snapshot, usage, err := s.knowledge.Prepare(ctx, model)
	if err != nil && s.cfg.Understanding.Strict {
		return snapshot, usage, err
	}
	if path != "" {
		if err := writeRuntime(path, savedKnowledge{Snapshot: snapshot, Hash: fingerprint(snapshot), Degraded: err != nil}); err != nil {
			return understanding.Snapshot{}, usage, snapshotPersistenceError{fmt.Errorf("保存续审知识快照: %w", err)}
		}
	}
	return snapshot, usage, err
}
