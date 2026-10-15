//go:build windows

package lifecycle

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func powershell(args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func splitCommand(line string) ([]string, error) {
	ptr, err := syscall.UTF16PtrFromString(line)
	if err != nil {
		return nil, err
	}
	var count int32
	argv, err := syscall.CommandLineToArgv(ptr, &count)
	if err != nil {
		return nil, err
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(argv))))
	args := make([]string, count)
	for i := range args {
		args[i] = syscall.UTF16ToString(argv[i][:])
	}
	return args, nil
}

func processes() ([]process, error) {
	// Only inspect the shells and review executable; never emit command lines
	// (which can contain unrelated users' data) into lifecycle output.
	cmd := powershell("-NoProfile", "-NonInteractive", "-Command", `[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false); $ErrorActionPreference='Stop'; $items=@(Get-CimInstance Win32_Process -Filter "Name='powershell.exe' OR Name='pwsh.exe' OR Name='gvs-pr-agent.exe'" | Select-Object ProcessId,ExecutablePath,CommandLine,@{n='Birth';e={$_.CreationDate.ToUniversalTime().Ticks.ToString()}}); ConvertTo-Json -InputObject $items -Compress`)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect processes: %w", err)
	}
	var rows []struct {
		ProcessId                          int
		ExecutablePath, CommandLine, Birth string
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(raw), "\ufeff")), &rows); err != nil {
		return nil, err
	}
	var out []process
	for _, row := range rows {
		if row.CommandLine == "" {
			continue
		}
		args, err := splitCommand(row.CommandLine)
		if err != nil {
			return nil, err
		}
		out = append(out, process{PID: row.ProcessId, Executable: row.ExecutablePath, Args: args, Birth: row.Birth})
	}
	return out, nil
}

func terminate(p process) error {
	// Recheck birth time and command before touching a process with a reused PID.
	all, err := processes()
	if err != nil {
		return err
	}
	for _, current := range all {
		if current.PID != p.PID {
			continue
		}
		if current.Birth != p.Birth || current.Executable != p.Executable || strings.Join(current.Args, "\x00") != strings.Join(p.Args, "\x00") {
			return fmt.Errorf("process identity changed for PID %d", p.PID)
		}
		proc, err := os.FindProcess(p.PID)
		if err != nil {
			return err
		}
		defer proc.Release()
		err = proc.Kill()
		if err == os.ErrProcessDone {
			return nil
		}
		return err
	}
	return nil
}

func launch(root, name string, output io.Writer) error {
	cmd := powershell("-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, "start-persistent.ps1"), "-Instance", name)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, output, output
	return cmd.Run()
}
