//go:build !windows

package singbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// findSingBoxProcesses finds all running sing-box processes by executable
// path, returning PID/PPID/zombie state. Mirrors
// backend/xray/process_unix.go's findXrayProcesses (this node only targets
// Linux, so the /proc path below is the one actually exercised; the ps
// fallback is kept for parity with xray's implementation).
func findSingBoxProcesses(executablePath string) ([]ProcessInfo, error) {
	absPath, err := filepath.Abs(executablePath)
	if err != nil {
		return nil, err
	}

	if runtime.GOOS == "linux" {
		if procs, perr := findSingBoxProcessesFromProc(absPath); perr == nil {
			return procs, nil
		}
	}

	cmd := exec.Command("ps", "-eo", "pid,ppid,state,command")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list processes: %w", err)
	}

	var processes []ProcessInfo
	lines := strings.Split(string(output), "\n")
	executableName := filepath.Base(absPath)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		pidStr := fields[0]
		ppidStr := fields[1]
		state := fields[2]
		cmdPath := fields[3]

		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(ppidStr)
		if err != nil {
			continue
		}

		match := false
		if procPath, err := getProcessPath(pid); err == nil {
			match = pathsMatch(procPath, absPath)
		}
		if !match {
			match = pathsMatch(cmdPath, absPath) || filepath.Base(cmdPath) == executableName
		}
		if !match {
			continue
		}

		isZombie := state == "Z" || state == "z"

		processes = append(processes, ProcessInfo{
			PID:      pid,
			PPID:     ppid,
			IsZombie: isZombie,
		})
	}

	return processes, nil
}

// findSingBoxProcessesFromProc scans /proc directly (Linux-only).
func findSingBoxProcessesFromProc(absPath string) ([]ProcessInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	var processes []ProcessInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}

		exePath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil {
			continue
		}
		if !pathsMatch(exePath, absPath) {
			continue
		}

		statPath := fmt.Sprintf("/proc/%d/stat", pid)
		data, err := os.ReadFile(statPath)
		if err != nil {
			continue
		}
		fields := strings.Fields(string(data))
		if len(fields) < 4 {
			continue
		}
		state := fields[2]
		ppid, err := strconv.Atoi(fields[3])
		if err != nil {
			continue
		}

		processes = append(processes, ProcessInfo{
			PID:      pid,
			PPID:     ppid,
			IsZombie: state == "Z" || state == "z",
		})
	}

	return processes, nil
}

func getProcessPath(pid int) (string, error) {
	procPath := fmt.Sprintf("/proc/%d/exe", pid)
	path, err := os.Readlink(procPath)
	if err == nil {
		return path, nil
	}

	psOutput, psErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if psErr != nil {
		return "", fmt.Errorf("failed to read process path: %w (ps fallback error: %v)", err, psErr)
	}

	cmdline := strings.TrimSpace(string(psOutput))
	if cmdline == "" {
		return "", fmt.Errorf("failed to read process path: empty command for pid %d", pid)
	}

	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return "", fmt.Errorf("failed to read process path: empty command for pid %d", pid)
	}

	return fields[0], nil
}

// killProcessTree kills a process and all its children on Unix.
func killProcessTree(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", pid, err)
	}

	_ = proc.Signal(syscall.SIGTERM)

	pgid, err := getProcessGroupID(pid)
	if err == nil && pgid != 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}

	_ = proc.Signal(syscall.SIGKILL)

	for i := 0; i < 10; i++ {
		if !isProcessRunning(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("process %d is still running after kill attempt", pid)
}

func getProcessGroupID(pid int) (int, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 5 {
		return 0, fmt.Errorf("invalid stat format")
	}

	pgid, err := strconv.Atoi(fields[4])
	if err != nil {
		return 0, err
	}

	return pgid, nil
}

func verifyProcessDead(pid int) error {
	if !isProcessRunning(pid) {
		return nil
	}
	return fmt.Errorf("process %d is still running", pid)
}

func isProcessRunning(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// pathsMatch compares two executable paths, resolving symlinks and
// absolutizing both.
func pathsMatch(candidate, target string) bool {
	if candidate == "" || target == "" {
		return false
	}

	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}

	if candidateAbs == targetAbs {
		return true
	}

	candidateReal, errA := filepath.EvalSymlinks(candidateAbs)
	targetReal, errB := filepath.EvalSymlinks(targetAbs)
	if errA == nil && errB == nil && candidateReal == targetReal {
		return true
	}

	return false
}
