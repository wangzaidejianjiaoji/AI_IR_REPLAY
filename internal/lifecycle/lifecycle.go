// Package lifecycle manages the existing platform launchers without loading
// review configuration or credentials for status/stop.
package lifecycle

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type process struct {
	PID        int
	Executable string
	Args       []string
	Birth      string
	Directory  string
}

func agentPath(root string) string {
	name := "gvs-pr-agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "bin", name)
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func option(args []string, name string) string {
	for i, arg := range args {
		if strings.EqualFold(arg, name) && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(strings.ToLower(arg), strings.ToLower(name)+"=") {
			return arg[len(name)+1:]
		}
	}
	return ""
}

// Match executable/script paths and the exact instance, never a PID file or a
// substring of a command. Legacy supervisors are discovered without migration.
func match(root, name string, p process) string {
	if p.PID == os.Getpid() {
		return ""
	}
	config := option(p.Args, "-config")
	if config != "" && samePath(p.Executable, agentPath(root)) {
		if !filepath.IsAbs(config) {
			dir := p.Directory
			if dir == "" {
				dir = root
			}
			config = filepath.Join(dir, config)
		}
		if samePath(config, filepath.Join(root, "configs", "config-"+name+".yaml")) {
			return "agent"
		}
	}
	if len(p.Args) < 3 {
		return ""
	}
	shell := strings.ToLower(filepath.Base(p.Args[0]))
	if shell == "bash" || shell == "bash.exe" {
		script := p.Args[1]
		if !filepath.IsAbs(script) && p.Directory != "" {
			script = filepath.Join(p.Directory, script)
		}
		if samePath(script, filepath.Join(root, "run-persistent.sh")) && len(p.Args) == 3 && p.Args[2] == name {
			return "supervisor"
		}
	}
	if shell == "powershell.exe" || shell == "pwsh.exe" {
		if samePath(option(p.Args, "-File"), filepath.Join(root, "run-persistent.ps1")) && option(p.Args, "-Instance") == name {
			return "supervisor"
		}
	}
	return ""
}

func selected(root, name string) ([]process, error) {
	all, err := processes()
	if err != nil {
		return nil, err
	}
	var found []process
	for _, p := range all {
		if match(root, name, p) != "" {
			found = append(found, p)
		}
	}
	return found, nil
}

// Run preserves the old launchers as the platform-specific start adapters.
// A restart proceeds to start only when every selected instance has stopped.
func Run(root, action, name string, output io.Writer) error {
	if action != "start" && action != "stop" && action != "restart" && action != "status" {
		return fmt.Errorf("unknown action %q", action)
	}
	names := []string{name}
	if name == "all" {
		names = []string{"gvs", "gvs-infer"}
	} else if name != "gvs" && name != "gvs-infer" {
		return fmt.Errorf("unknown instance %q", name)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if action == "start" {
		return launch(root, name, output)
	}
	var failures []error
	for _, instance := range names {
		if action == "status" {
			found, err := selected(root, instance)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if len(found) == 0 {
				fmt.Fprintf(output, "%s: stopped\n", instance)
				continue
			}
			fmt.Fprintf(output, "%s:", instance)
			for _, p := range found {
				fmt.Fprintf(output, " %s=%d", match(root, instance, p), p.PID)
			}
			fmt.Fprintln(output)
		} else if err := stop(root, instance, output); err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	if action == "restart" {
		return launch(root, name, output)
	}
	return nil
}

func stop(root, name string, output io.Writer) error {
	found, err := selected(root, name)
	if err != nil {
		return err
	}
	// Stop restart loops first. Then rescan to catch a child launched between
	// discovery and supervisor termination. Never force past the timeout.
	for _, role := range []string{"supervisor", "agent"} {
		if role == "agent" {
			found, err = selected(root, name)
			if err != nil {
				return err
			}
		}
		for _, p := range found {
			if match(root, name, p) != role {
				continue
			}
			if err := terminate(p); err != nil {
				return fmt.Errorf("stop %s %d: %w", role, p.PID, err)
			}
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		found, err = selected(root, name)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			fmt.Fprintf(output, "%s: stopped\n", name)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not stop within 15s; restart cancelled, inspect processes/logs", name)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
