package understanding

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"gvs-pr-agent/internal/llm"
)

type incrementalModel struct {
	requests []string
	failAt   int
}

func TestIncrementalPatchReplacesOnlyAnchoredKnowledge(t *testing.T) {
	current := "# Module\nOld lifecycle.\n\n# Unrelated\nKeep this invariant."
	got, err := applyKnowledgePatch(current, "# Module\nOld lifecycle.", `{"replacements":[{"old":"Old lifecycle.","new":"Updated lifecycle."}],"append":""}`)
	if err != nil || got != "# Module\nUpdated lifecycle.\n\n# Unrelated\nKeep this invariant." {
		t.Fatalf("got=%q err=%v", got, err)
	}
	for _, response := range []string{
		`{"replacements":[{"old":"Keep this invariant.","new":"Changed without evidence"}]}`,
		`{"replacements":[{"old":"missing","new":"Changed"}]}`,
		`{"replacements":[],"append":"<!-- SOURCE-SHA: forged -->"}`,
		`{"replacements":[]} trailing output`,
		`{"append":"missing edit contract"}`,
	} {
		if _, err := applyKnowledgePatch(current, "# Module\nOld lifecycle.", response); err == nil {
			t.Fatalf("accepted invalid patch: %s", response)
		}
	}
	if _, err := applyKnowledgePatch("duplicate duplicate", "duplicate", `{"replacements":[{"old":"duplicate","new":"x"}]}`); err == nil {
		t.Fatal("ambiguous replacement accepted")
	}
}

func TestEvidenceBatchesPreserveUnicodeLongLinesAndEscapedInput(t *testing.T) {
	input := strings.Repeat("界\x00\"\\", 15000)
	parts := evidenceBatches(input)
	if strings.Join(parts, "") != input {
		t.Fatal("evidence changed")
	}
	for _, part := range parts {
		encoded, _ := json.Marshal(part)
		if !utf8.ValidString(part) || len(encoded) > 24000 {
			t.Fatal("batch exceeds encoded budget or splits UTF-8")
		}
	}
}

func (m *incrementalModel) Chat(_ context.Context, _ string, messages []llm.Message) (string, llm.Usage, error) {
	m.requests = append(m.requests, messages[1].Content)
	usage := llm.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}
	if len(m.requests) == m.failAt {
		return "", usage, errors.New("interrupted")
	}
	return `{"replacements":[],"append":"Incrementally verified change."}`, usage, nil
}

func TestPrepareIncrementalLargeInputPreservesKnowledgeAndAllEvidence(t *testing.T) {
	cfg := understandingConfig(t)
	old := "<!-- SOURCE-REPOSITORY: " + cfg.SourceRepository + " -->\n<!-- SOURCE-SHA: old -->\n# Knowledge\n" + strings.Repeat("Unchanged lifecycle invariant.\n", 2500)
	if err := os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.DocumentPath, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	delta := "diff --git a/core.cpp b/core.cpp\n" + strings.Repeat("+新增数据\n", 13000) + "+LAST-EVIDENCE\n"
	model := &incrementalModel{}
	m := New(cfg, &fakeRepo{head: "new", diff: delta}, model, "", nil)
	snapshot, usage, err := m.Prepare(context.Background(), "model")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Updated || snapshot.SourceSHA != "new" || !strings.Contains(snapshot.Document, strings.Repeat("Unchanged lifecycle invariant.\n", 2500)) {
		t.Fatal("incremental update discarded unchanged knowledge or failed to advance version")
	}
	var evidence strings.Builder
	for i, request := range model.requests {
		if len(request) > 60000 {
			t.Fatalf("unbounded request: %d", len(request))
		}
		var payload struct {
			Evidence string `json:"evidence"`
			Scope    string `json:"continuation_scope"`
		}
		if err := json.Unmarshal([]byte(request), &payload); err != nil {
			t.Fatal(err)
		}
		if i > 0 && !strings.Contains(payload.Scope, "core.cpp") {
			t.Fatal("continuation lost its source file")
		}
		evidence.WriteString(payload.Evidence)
	}
	if evidence.String() != delta {
		t.Fatal("evidence omitted, duplicated, or truncated")
	}
	if len(model.requests) < 2 || usage.TotalTokens != 10*len(model.requests) {
		t.Fatalf("usage=%+v calls=%d", usage, len(model.requests))
	}
}

func TestIncrementalContextIncludesRelevantKnowledgeBeyondOverview(t *testing.T) {
	doc := "# Overview\n" + strings.Repeat("Generic unrelated description.\n", 1800) + "\n# StreamLifetime\nDestroy StreamLifetime only after joining the CUDA worker.\n" + strings.Repeat("Other unrelated content.\n", 700)
	context := incrementalContext(doc, "diff --git a/StreamLifetime.cpp b/StreamLifetime.cpp\n-join worker\n+return\n")
	if !strings.Contains(context, "# Overview") || !strings.Contains(context, "Destroy StreamLifetime only after joining the CUDA worker.") {
		t.Fatal("relevant existing invariant was excluded from update context")
	}
}

func TestPrepareLongDiffLinesRetainOriginalAddedOrRemovedPrefix(t *testing.T) {
	cfg := understandingConfig(t)
	os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0755)
	os.WriteFile(cfg.DocumentPath, []byte("<!-- SOURCE-REPOSITORY: "+cfg.SourceRepository+" -->\n<!-- SOURCE-SHA: old -->\n# Knowledge"), 0600)
	delta := "diff --git a/config.json b/config.json\n@@ -1 +1 @@\n-" + strings.Repeat("old", 18000) + "\n+" + strings.Repeat("new", 18000) + "\n"
	model := &incrementalModel{}
	if _, _, err := New(cfg, &fakeRepo{head: "new", diff: delta}, model, "", nil).Prepare(context.Background(), "model"); err != nil {
		t.Fatal(err)
	}
	offset := 0
	for _, request := range model.requests {
		var payload struct {
			Evidence string `json:"evidence"`
			Scope    string `json:"continuation_scope"`
		}
		if err := json.Unmarshal([]byte(request), &payload); err != nil {
			t.Fatal(err)
		}
		if offset > 0 && delta[offset-1] != '\n' {
			lineStart := strings.LastIndexByte(delta[:offset], '\n') + 1
			prefix := delta[lineStart : lineStart+1]
			if !strings.Contains(payload.Scope, `line_prefix="`+prefix+`"`) {
				t.Fatalf("long line evidence lost diff sign: offset=%d scope=%q", offset, payload.Scope)
			}
		}
		offset += len(payload.Evidence)
	}
}

func TestPrepareIncrementalFailureDoesNotAdvanceOrPartiallyWrite(t *testing.T) {
	cfg := understandingConfig(t)
	old := "<!-- SOURCE-REPOSITORY: " + cfg.SourceRepository + " -->\n<!-- SOURCE-SHA: old -->\n# Preserved"
	os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0755)
	os.WriteFile(cfg.DocumentPath, []byte(old), 0600)
	model := &incrementalModel{failAt: 2}
	snapshot, usage, err := New(cfg, &fakeRepo{head: "new", diff: strings.Repeat("+changed\n", 20000)}, model, "", nil).Prepare(context.Background(), "model")
	raw, readErr := os.ReadFile(cfg.DocumentPath)
	if err == nil || readErr != nil || string(raw) != old || snapshot.SourceSHA != "old" || usage.TotalTokens != 20 {
		t.Fatalf("partial update escaped: snapshot=%+v usage=%+v err=%v", snapshot, usage, err)
	}
}
