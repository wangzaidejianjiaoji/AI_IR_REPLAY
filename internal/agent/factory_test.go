package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
	"gvs-pr-agent/internal/rules"
)

func TestBuildEnabledElevenRoles(t *testing.T) {
	roles := []string{"security", "performance", "architect", "ui", "ai_tool", "standard", "test", "ai", "concurrency", "resource", "dataflow"}
	cfg := map[string]config.Agent{}
	for _, role := range roles {
		cfg[role] = config.Agent{Enabled: true, Priority: 1}
	}
	got := BuildEnabled(cfg, nil, slog.Default())
	if len(got) != len(roles) {
		t.Fatalf("built %d roles, want %d", len(got), len(roles))
	}
	for i, a := range got {
		if a.Role() != roles[i] || a.Priority() != 1 {
			t.Errorf("role %d: %s priority %d", i, a.Role(), a.Priority())
		}
	}
	cfg["resource"] = config.Agent{Enabled: false}
	delete(cfg, "dataflow")
	cfg["concurrency"] = config.Agent{Enabled: true}
	got = BuildEnabled(cfg, nil, slog.Default())
	if len(got) != 9 || got[8].Role() != "concurrency" || got[8].Priority() != 2 {
		t.Fatalf("disabled/missing roles or default priority not respected: %v", got)
	}
}

func TestSpecialistRulesReachReviewPrompt(t *testing.T) {
	for _, ruleFile := range []string{"review-rules-gvs.yaml", "review-rules-gvs-infer.yaml"} {
		for _, role := range []string{"concurrency", "resource", "dataflow"} {
			t.Run(ruleFile+"/"+role, func(t *testing.T) {
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				store, err := rules.NewStore("../../configs/"+ruleFile, "", "", logger)
				if err != nil {
					t.Fatal(err)
				}
				if len(store.Current().Agents[role]) == 0 {
					t.Fatalf("specialist rules missing for %s", role)
				}
				wantRules := store.Current().ForAgent(role, nil)
				var system string
				client := newLLM(t, func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Messages []llm.Message `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					for _, m := range request.Messages {
						if m.Role == "system" {
							system = m.Content
						}
					}
					_ = json.NewEncoder(w).Encode(chatResp(`[{"severity":"high","file":"stream/a.go","line":2,"problem":"p"}]`))
				})
				agents := BuildEnabled(map[string]config.Agent{role: {Enabled: true, Priority: 1}}, client, logger)
				if len(agents) != 1 {
					t.Fatalf("specialist %s not constructed", role)
				}
				res := agents[0].Review(context.Background(), Task{Model: "m", Rules: wantRules})
				if res.Err != nil {
					t.Fatal(res.Err)
				}
				for _, rule := range wantRules {
					if !strings.Contains(system, rule) {
						t.Errorf("missing prompt rule: %s", rule)
					}
				}
				if len(res.Issues) != 1 || res.Issues[0].AgentRole != role {
					t.Fatalf("lost specialist attribution: %+v", res)
				}
			})
		}
	}
}

func TestShippedConfigsBuildElevenRoles(t *testing.T) {
	for _, name := range []string{"CODEUP_ORG_ID", "CODEUP_TOKEN", "KIMI_API_KEY", "GITHUB_TOKEN"} {
		t.Setenv(name, "fixture")
	}
	for _, name := range []string{"AGENT_ADMIN_TOKEN", "AGENT_SERVER_ADDR", "AGENT_REVIEW_DRY_RUN"} {
		t.Setenv(name, "")
	}
	paths, err := filepath.Glob("../../configs/config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 5 {
		t.Fatalf("expected five shipped configs, got %d", len(paths))
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := BuildEnabled(cfg.Agents, nil, slog.Default()); len(got) != 11 {
				t.Fatalf("config created %d roles, want 11", len(got))
			}
		})
	}
}
