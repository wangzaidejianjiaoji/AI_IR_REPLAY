package coordinator

import (
	"encoding/json"
	"sort"

	"gvs-pr-agent/internal/agent"
)

// sortIssues makes candidate identities and reflection independent of job and
// model response completion order, including findings at identical positions.
func sortIssues(issues []agent.Issue) {
	sort.Slice(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if ar, br := agent.SeverityRank(a.Severity), agent.SeverityRank(b.Severity); ar != br {
			return ar > br
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		ab, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if string(ab) != string(bb) {
			return string(ab) < string(bb)
		}
		if a.AgentRole != b.AgentRole {
			return a.AgentRole < b.AgentRole
		}
		return a.AgentName < b.AgentName
	})
}
