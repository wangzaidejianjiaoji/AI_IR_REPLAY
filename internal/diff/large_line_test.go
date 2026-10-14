package diff

import (
	"strings"
	"testing"
)

func TestParseRetainsFilesAfterLongSourceLine(t *testing.T) {
	raw := "diff --git a/long.go b/long.go\n@@ -1 +1 @@\n-old\n+" + strings.Repeat("x", 1024*1024+100) + "\ndiff --git a/tail.go b/tail.go\n@@ -1 +1 @@\n-old\n+TAIL\n"
	files := Parse(raw)
	if len(files) != 2 || files[1].Path != "tail.go" || !strings.Contains(files[0].Patch, strings.Repeat("x", 1024*1024+100)) {
		t.Fatal("parser silently lost long line or trailing files")
	}
}
