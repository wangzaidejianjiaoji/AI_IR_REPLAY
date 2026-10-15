package review

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
)

func TestFocusedMRRestartRunsOnlyPreviouslyFailedModelWork(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	var architectCalls, securityCalls atomic.Int32
	var securityAvailable atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if len(req.Messages) == 0 {
			t.Error("missing prompt")
			http.Error(w, "missing prompt", 400)
			return
		}
		if strings.Contains(req.Messages[0].Content, "角色是「架构师」") {
			architectCalls.Add(1)
		} else {
			securityCalls.Add(1)
			if !securityAvailable.Load() {
				http.Error(w, "provider offline", 503)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Content: "[]"}}}})
	}))
	defer model.Close()
	cfg.LLM.BaseURL = model.URL
	cfg.Review.CheckpointDir = t.TempDir()
	cfg.Review.MaxAgentConcurrency = 2
	cfg.Agents = map[string]config.Agent{"architect": {Enabled: true, Priority: 1}, "security": {Enabled: true, Priority: 1}}
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err == nil {
		t.Fatal("partial failure accepted")
	}
	_, _, comments := cu.snapshot()
	if len(comments[8]) != 0 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "" {
		t.Fatal("failed round published or advanced baseline")
	}
	securityAvailable.Store(true)
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	if architectCalls.Load() != 1 || securityCalls.Load() != 2 {
		t.Fatalf("successful work repeated: architect=%d security=%d", architectCalls.Load(), securityCalls.Load())
	}
	_, _, comments = cu.snapshot()
	if len(comments[8]) != 1 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "full-source-sha" {
		t.Fatal("completed resume did not publish exactly once")
	}
}
