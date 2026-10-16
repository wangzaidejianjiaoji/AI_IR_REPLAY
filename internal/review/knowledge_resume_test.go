package review

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
)

func TestFocusedMRResumePinsKnowledgeAndCompletedAnalysis(t *testing.T) {
	for _, degraded := range []bool{false, true} {
		name := "updated knowledge"
		if degraded {
			name = "nonstrict degraded knowledge"
		}
		t.Run(name, func(t *testing.T) {
			cu, cfg, _ := focusedFixture(t)
			var documentCalls, analysisCalls, specialistCalls atomic.Int32
			expectedKnowledge := "PINNED-main-one"
			expectedSHA := "main-one"
			if degraded {
				expectedKnowledge = "PINNED-seed-old"
				expectedSHA = "seed-old"
			}
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "bad request", 400)
					return
				}
				if len(req.Messages) < 2 {
					t.Error("missing model messages")
					http.Error(w, "bad request", 400)
					return
				}
				system, user := req.Messages[0].Content, req.Messages[1].Content
				content := "[]"
				switch {
				case strings.Contains(system, "全局代码理解维护 Agent"):
					documentCalls.Add(1)
					if degraded {
						http.Error(w, "knowledge provider temporarily unavailable", 503)
						return
					}
					content = "# PINNED-main-one\n\nDocument prepared before interruption."
				case strings.Contains(system, "代码变更意图分析器"):
					analysisCalls.Add(1)
					if !strings.Contains(user, expectedKnowledge) || strings.Contains(user, "UNRELATED-main-two") {
						t.Errorf("analysis consumed wrong knowledge: %s", user)
					}
					content = `{"summary":"facts-from-pinned-knowledge","intent":"preserve pinned context","affected_modules":["gvs"],"behavior_constraints":["stable contract"],"comment_logic_mismatches":[]}`
				case strings.Contains(system, "最终代码审查统筹确认 Agent"):
					t.Error("empty specialist findings unexpectedly requested confirmation")
					http.Error(w, "unexpected confirmation", 400)
					return
				default:
					if !strings.Contains(user, "facts-from-pinned-knowledge") {
						t.Errorf("specialist lost completed global analysis: %s", user)
					}
					if specialistCalls.Add(1) == 1 {
						http.Error(w, "specialist interrupted", 503)
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
			}))
			defer model.Close()
			cfg.LLM.BaseURL = model.URL
			cfg.Understanding = config.UnderstandingConfig{Enabled: true, Strict: !degraded, SourceRepository: "lzvsg/gvs", SourceBranch: "main", DocumentPath: filepath.Join(t.TempDir(), "knowledge.md")}
			initial := "<!-- SOURCE-REPOSITORY: lzvsg/gvs -->\n<!-- SOURCE-SHA: seed-old -->\n# PINNED-seed-old"
			if err := os.WriteFile(cfg.Understanding.DocumentPath, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}
			cu.setBranches(branch("main", "main-one", true))
			if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err == nil {
				t.Fatal("interrupted specialist review unexpectedly succeeded")
			}
			_, _, comments := cu.snapshot()
			if len(comments) != 0 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "" {
				t.Fatal("interrupted review published or advanced baseline")
			}
			if documentCalls.Load() != 1 || analysisCalls.Load() != 1 || specialistCalls.Load() != 1 {
				t.Fatalf("initial phase calls: document=%d analysis=%d specialist=%d", documentCalls.Load(), analysisCalls.Load(), specialistCalls.Load())
			}

			// Another review can update the shared local knowledge while this MR is
			// suspended. Neither that file nor main's new HEAD may change its prompts.
			cu.setBranches(branch("main", "main-two", true))
			advanced := "<!-- SOURCE-REPOSITORY: lzvsg/gvs -->\n<!-- SOURCE-SHA: main-two -->\n# UNRELATED-main-two"
			if err := os.WriteFile(cfg.Understanding.DocumentPath, []byte(advanced), 0600); err != nil {
				t.Fatal(err)
			}
			if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
				t.Fatal(err)
			}
			if documentCalls.Load() != 1 || analysisCalls.Load() != 1 || specialistCalls.Load() != 2 {
				t.Fatalf("restart repeated completed phases: document=%d analysis=%d specialist=%d", documentCalls.Load(), analysisCalls.Load(), specialistCalls.Load())
			}
			_, _, comments = cu.snapshot()
			if len(comments[8]) != 1 {
				t.Fatalf("resumed review published %d reports", len(comments[8]))
			}
			report, _ := comments[8][0]["content"].(string)
			if !strings.Contains(report, expectedSHA) || strings.Contains(report, "main-two") || !strings.Contains(report, "facts-from-pinned-knowledge") {
				t.Fatalf("report lost pinned knowledge or analysis: %s", report)
			}
			if degraded && !strings.Contains(report, "降级") {
				t.Fatalf("degraded knowledge was presented as current: %s", report)
			}
			if got := baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8"); got != "full-source-sha" {
				t.Fatalf("resumed baseline=%q", got)
			}
		})
	}
}
