package config

import "testing"

func TestRejectMalformedRuntimeConfiguration(t *testing.T) {
	for _, y := range []string{
		"scheduler: {interval: '-1s'}", "scheduler: {interval: '0s'}", "scheduler: {jitter: '-1s'}",
		"scheduler: {interval: 'invalid'}", "agents: {made_up: {enabled: true}}",
		"llm: {models: {small: 'm', medium: '', large: 'm'}}",
		"review: {max_agent_concurrency: -1}", "review: {max_files_per_review: -1}",
		"llm: {timeout: '0s'}", "review: {max_llm_calls: -1}",
		"review: {round_timeout: 'bad'}", "review: {max_round_tokens: -1}",
		"review: {active_hours: {start: '25:00', end: '21:00'}}",
		"llm: {model_thresholds: {small: 1000, medium: 500}}",
		"server: {addr: '0.0.0.0:8081'}",
	} {
		t.Run(y, func(t *testing.T) {
			if _, err := Load(writeConfig(t, y)); err == nil {
				t.Fatalf("invalid configuration accepted: %s", y)
			}
		})
	}
}

func TestRejectUnknownYAMLField(t *testing.T) {
	if _, err := Load(writeConfig(t, "review: {max_agent_concurency: 2}")); err == nil {
		t.Fatal("misspelled YAML field silently ignored")
	}
}

func TestRejectAdditionalYAMLDocument(t *testing.T){
	if _,err:=Load(writeConfig(t,"scheduler: {interval: '30m'}\n---\nreview: {max_llm_calls: -1}\n"));err==nil{t.Fatal("second YAML document silently ignored")}
}
