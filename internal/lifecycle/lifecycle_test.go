package lifecycle

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gvs-pr-agent/internal/progress"
)

// A copied test executable serves as an isolated agent/controller for the real
// launcher and supervisor scripts. It never loads review configs or uses HTTP.
func TestMain(m *testing.M) {
	if os.Getenv("LIFECYCLE_TEST_HELPER") == "1" && len(os.Args) > 1 {
		if os.Args[1] == "-config" {
			for {
				time.Sleep(time.Second)
			}
		}
		if os.Args[1] == "-action" {
			flags := flag.NewFlagSet("helper", flag.ExitOnError)
			action := flags.String("action", "", "")
			name := flags.String("instance", "all", "")
			_ = flags.Parse(os.Args[1:])
			exe, _ := os.Executable()
			if err := Run(filepath.Dir(filepath.Dir(exe)), *action, *name, os.Stdout); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestMatchesOnlySelectedWorkspaceAndInstance(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	script := filepath.Join(root, "run-persistent.sh")
	cases := []struct {
		name string
		p    process
		want string
	}{
		{"supervisor", process{Args: []string{"bash", script, "gvs"}}, "supervisor"},
		{"other instance", process{Args: []string{"bash", script, "gvs-infer"}}, ""},
		{"other workspace", process{Args: []string{"bash", filepath.Join(other, "run-persistent.sh"), "gvs"}}, ""},
		{"not a script invocation", process{Args: []string{"editor", script, "gvs"}}, ""},
		{"Windows supervisor", process{Args: []string{"powershell.exe", "-NoProfile", "-File", filepath.Join(root, "run-persistent.ps1"), "-Instance", "gvs"}}, "supervisor"},
		{"Windows other workspace", process{Args: []string{"powershell.exe", "-File", filepath.Join(other, "run-persistent.ps1"), "-Instance", "gvs"}}, ""},
		{"agent", process{Executable: agentPath(root), Args: []string{agentPath(root), "-config", "configs/config-gvs.yaml"}}, "agent"},
		{"other agent", process{Executable: agentPath(other), Args: []string{agentPath(other), "-config", "configs/config-gvs.yaml"}}, ""},
		{"manual agent", process{Executable: agentPath(root), Args: []string{agentPath(root), "-config", "configs/config-gvs.yaml", "-repo", "repo", "-mr", "7"}}, "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := match(root, "gvs", tc.p); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusAndStopDoNotTrustStalePIDFileOrNeedCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data", "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	// Deliberately points to an unrelated PID, which must not be trusted.
	if err := os.WriteFile(filepath.Join(root, "data", "runtime", "gvs.supervisor.pid"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"status", "stop", "stop"} {
		var output bytes.Buffer
		if err := Run(root, action, "all", &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "gvs: stopped") || !strings.Contains(output.String(), "gvs-infer: stopped") {
			t.Fatalf("unexpected output: %s", &output)
		}
	}
}

func TestRealSupervisorStartDuplicateStopAndRestart(t *testing.T) {
	root := t.TempDir()
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"bin", "configs", "local/AI_IR_REPLAY"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".ps1"
	}
	for _, name := range []string{"start-persistent", "run-persistent"} {
		raw, err := os.ReadFile(filepath.Join(repository, name+ext))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+ext), raw, 0700); err != nil {
			t.Fatal(err)
		}
	}
	exe, _ := os.Executable()
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath(root), raw, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "configs", "config-gvs.yaml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIFECYCLE_TEST_HELPER", "1")
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
		if err := os.WriteFile(filepath.Join(root, "local", "AI_IR_REPLAY", "runtime-env.ps1"), []byte(`$env:KIMI_API_KEY='mock'; $env:CODEUP_TOKEN='mock'; $env:CODEUP_ORG_ID='mock'`), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := exec.LookPath("flock"); err != nil {
			t.Skip("requires Linux flock")
		}
		credentials := filepath.Join(root, "credentials.sh")
		if err := os.WriteFile(credentials, []byte("export KIMI_API_KEY=mock CODEUP_TOKEN=mock CODEUP_ORG_ID=mock\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AI_IR_RUNTIME_ENV", credentials)
	}
	t.Cleanup(func() {
		if err := Run(root, "stop", "gvs", &bytes.Buffer{}); err != nil {
			t.Error(err)
		}
	})
	var out bytes.Buffer
	for _, action := range []string{"start", "start", "stop", "restart"} {
		if err := Run(root, action, "gvs", &out); err != nil {
			t.Fatalf("%s: %v\n%s", action, err, &out)
		}
		if action == "stop" {
			continue
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			found, err := selected(root, "gvs")
			if err != nil {
				t.Fatal(err)
			}
			supervisors, agents := 0, 0
			for _, p := range found {
				if match(root, "gvs", p) == "supervisor" {
					supervisors++
				} else {
					agents++
				}
			}
			if supervisors == 1 && agents == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: supervisors=%d agents=%d\n%s", action, supervisors, agents, &out)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func TestRejectsInvalidActionAndInstance(t *testing.T) {
	for _, pair := range [][2]string{{"delete", "all"}, {"stop", "other"}} {
		if err := Run(t.TempDir(), pair[0], pair[1], &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}

func TestLifecycleAgentHelper(t *testing.T) {
	if os.Getenv("LIFECYCLE_TEST_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestStopTerminatesSelectedAgentAndPreservesOtherWorkspace(t *testing.T) {
	start := func(root string) (*exec.Cmd, <-chan error) {
		t.Helper()
		source, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(agentPath(root), raw, 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(agentPath(root), "-test.run=TestLifecycleAgentHelper", "--", "-config", "configs/config-gvs.yaml")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "LIFECYCLE_TEST_HELPER=1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd, done
	}
	root := t.TempDir()
	selectedAgent, selected := start(root)
	_, other := start(t.TempDir())
	statusPath := filepath.Join(root, "data", "runtime", "gvs.status.json")
	if err := os.MkdirAll(filepath.Dir(statusPath), 0700); err != nil {
		t.Fatal(err)
	}
	status, _ := json.Marshal(progress.Snapshot{PID: selectedAgent.Process.Pid, State: "reviewing", Task: "MR-8", Title: "Audit streamer", StartedAt: time.Now(), UpdatedAt: time.Now(), TotalTokens: 150})
	if err := os.WriteFile(statusPath, status, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(root, "status", "gvs", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "agent=") {
		t.Fatalf("agent missing from status: %s", &out)
	}
	if !strings.Contains(out.String(), "审核对象") || !strings.Contains(out.String(), "Audit streamer") || !strings.Contains(out.String(), "150 (0+0)") {
		t.Fatalf("runtime details missing: %s", &out)
	}
	if err := Run(root, "stop", "gvs", &out); err != nil {
		t.Fatal(err)
	}
	select {
	case <-selected:
	case <-time.After(3 * time.Second):
		t.Fatal("selected agent remains")
	}
	select {
	case <-other:
		t.Fatal("stopped other workspace")
	default:
	}
}
