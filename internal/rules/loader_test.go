package rules

import (
	"strings"
	"testing"
)

func TestTopicRulesBelongToIndependentSpecialists(t *testing.T) {
	d := &Document{Global: []string{"global"}, Agents: map[string][]string{
		"concurrency": {"concurrency-only"}, "resource": {"resource-only"}, "dataflow": {"dataflow-only"},
	}}
	for _, topic := range []string{"concurrency", "resource", "dataflow"} {
		if !strings.Contains(strings.Join(d.ForAgent(topic, nil), "\n"), topic+"-only") {
			t.Errorf("specialist %s lost its own rules", topic)
		}
		for _, role := range []string{"architect", "security", "performance", "standard", "test", "ai", "ui", "ai_tool"} {
			if strings.Contains(strings.Join(d.ForAgent(role, nil), "\n"), topic+"-only") {
				t.Errorf("topic %s unexpectedly forwarded to %s", topic, role)
			}
		}
	}
}
