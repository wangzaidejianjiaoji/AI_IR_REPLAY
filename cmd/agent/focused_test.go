package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFocusedCLIHelper(t *testing.T) {
	if os.Getenv("FOCUSED_CLI_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("agent", flag.ExitOnError)
	main()
}

func TestFocusedCLIRejectsInvalidSelections(t *testing.T) {
	for _, args := range [][]string{
		{"-repo", "lzvsg/gvs"}, {"-mr", "8"}, {"-mr", "0"},
		{"-repo", "lzvsg/gvs", "-mr", "-1"},
		{"-repo", "lzvsg/gvs", "-mr", "8", "-diff", "missing.patch"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestFocusedCLIHelper", "--"}, args...)...)
			cmd.Env = append(os.Environ(), "FOCUSED_CLI_HELPER=1")
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "-repo") || strings.Contains(string(output), "加载配置失败") {
				t.Fatalf("invalid flags were not rejected before loading config: err=%v output=%s", err, output)
			}
		})
	}
}

func TestValidateFocusedFlags(t *testing.T) {
	for _, test := range []struct {
		name, repo, diff string
		id               int
		wantError        bool
	}{
		{"default", "", "", 0, false},
		{"focused", "lzvsg/gvs", "", 8, false},
		{"repo-only", "lzvsg/gvs", "", 0, true},
		{"mr-only", "", "", 8, true},
		{"negative-mr", "lzvsg/gvs", "", -1, true},
		{"diff-conflict", "lzvsg/gvs", "local.patch", 8, true},
		{"whitespace-repo", " ", "", 8, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateFocusedFlags(test.repo, test.id, test.diff); (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}
