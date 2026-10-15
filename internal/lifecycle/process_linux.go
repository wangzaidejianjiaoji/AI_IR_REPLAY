package lifecycle

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func linuxProcess(pid int) (process, bool) {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return process{}, false
	}
	// comm may contain spaces or parentheses; fields after its final ')' begin
	// at state (field 3). starttime is field 22.
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return process{}, false
	}
	fields := strings.Fields(string(stat)[end+1:])
	if len(fields) < 20 || fields[0] == "Z" {
		return process{}, false
	}
	raw, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if err != nil || len(raw) == 0 {
		return process{}, false
	}
	executable, _ := os.Readlink(filepath.Join(base, "exe"))
	directory, _ := os.Readlink(filepath.Join(base, "cwd"))
	return process{PID: pid, Executable: executable, Directory: directory, Birth: fields[19], Args: strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")}, true
}

func processes() ([]process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if p, ok := linuxProcess(pid); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func terminate(p process) error {
	current, ok := linuxProcess(p.PID)
	if !ok {
		return nil
	}
	if current.Birth != p.Birth || strings.Join(current.Args, "\x00") != strings.Join(p.Args, "\x00") {
		return fmt.Errorf("process identity changed for PID %d", p.PID)
	}
	err := syscall.Kill(p.PID, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func launch(root, name string, output io.Writer) error {
	cmd := exec.Command("bash", filepath.Join(root, "start-persistent.sh"), name)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, output, output
	return cmd.Run()
}
