package config

import "testing"

func TestAcceptSpecialistRoles(t *testing.T) {
	for _, role := range []string{"concurrency", "resource", "dataflow"} {
		if _, err := Load(writeConfig(t, "agents: {"+role+": {enabled: true, priority: 1}}")); err != nil {
			t.Errorf("rejected %s: %v", role, err)
		}
	}
}
