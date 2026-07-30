//go:build !windows

package openvpn

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

// ProcessInfo holds information about a process found by
// findOpenVPNProcesses.
type ProcessInfo struct {
	PID      int
	PPID     int
	IsZombie bool
}

// findOpenVPNProcesses finds running openvpn processes matching
// executablePath whose command line references configPath as their
// "--config" argument.
//
// Unlike backend/singbox's findSingBoxProcesses (which only ever has to find
// at most one sing-box process for the whole backend), this backend runs one
// openvpn process per configured instance, so matching by executable path
// alone would risk attributing (and potentially killing) a sibling
// instance's healthy process during cleanup. Matching the config path out of
// /proc/<pid>/cmdline disambiguates between instances.
func findOpenVPNProcesses(executablePath, configPath string) ([]ProcessInfo, error) {
	absExe, err := filepath.Abs(executablePath)
	if err != nil {
		return nil, err
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}

	if runtime.GOOS == "linux" {
		if procs, perr := findOpenVPNProcessesFromProc(absExe, absConfig); perr == nil {
			return procs, nil
		}
	}

	cmd := exec.Command("ps", "-eo", "pid,ppid,state,command")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list processes: %w", err)
	}

	var processes []ProcessInfo
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		state := fields[2]
		commandLine := strings.Join(fields[3:], " ")

		if !pathsMatch(fields[3], absExe) && filepath.Base(fields[3]) != filepath.Base(absExe) {
			continue
		}
		if !strings.Contains(commandLine, absConfig) {
			continue
		}

		processes = append(processes, ProcessInfo{PID: pid, PPID: ppid, IsZombie: state == "Z" || state == "z"})
	}

	return processes, nil
}

// findOpenVPNProcessesFromProc scans /proc directly (Linux-only).
func findOpenVPNProcessesFromProc(absExe, absConfig string) ([]ProcessInfo, error) {
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
		if err != nil || !pathsMatch(exePath, absExe) {
			continue
		}

		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		matched := false
		for _, a := range strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00") {
			if a == absConfig {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(statData))
		if len(fields) < 4 {
			continue
		}
		state := fields[2]
		ppid, err := strconv.Atoi(fields[3])
		if err != nil {
			continue
		}

		processes = append(processes, ProcessInfo{PID: pid, PPID: ppid, IsZombie: state == "Z" || state == "z"})
	}

	return processes, nil
}

// killProcessTree, getProcessGroupID, verifyProcessDead, isProcessRunning,
// and pathsMatch mirror backend/singbox/process_unix.go (generic
// process-management helpers with no openvpn-specific logic - this repo does
// not share such helpers between backend packages, see log.go's doc
// comment).

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
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 5 {
		return 0, fmt.Errorf("invalid stat format")
	}
	return strconv.Atoi(fields[4])
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
	return proc.Signal(syscall.Signal(0)) == nil
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
	return errA == nil && errB == nil && candidateReal == targetReal
}
