package procwatch

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// OsProvider is the real Provider using ps and lsof on macOS/Linux.
type OsProvider struct{}

func (OsProvider) Info(pid int) (*ProcInfo, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "pid=,lstart=,comm=").Output()
	if err != nil {
		return nil, nil // process gone — ps exits non-zero when PID not found
	}
	fields := strings.Fields(string(bytes.TrimSpace(out)))
	// lstart on macOS: "Mon Jan  2 15:04:05 2006" = 5 tokens after pid
	// pid(1) + lstart(5) + comm(1+) = at least 7 tokens
	if len(fields) < 7 {
		return nil, fmt.Errorf("procwatch: ps: unexpected output for pid %d: %q", pid, string(out))
	}
	startTime := strings.Join(fields[1:6], " ")
	command := strings.Join(fields[6:], " ")
	return &ProcInfo{PID: pid, StartTime: startTime, Command: command}, nil
}

func (OsProvider) IsOrphaned(pid int) (bool, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=,tty=").Output()
	if err != nil {
		return false, nil
	}
	fields := strings.Fields(string(bytes.TrimSpace(out)))
	if len(fields) < 2 {
		return false, nil
	}
	ppid, _ := strconv.Atoi(fields[0])
	tty := fields[1]
	return ppid == 1 || tty == "??" || tty == "", nil
}

func (OsProvider) MatchByDir(dir string) ([]int, error) {
	// Step 1: find PIDs of processes whose command contains "claude"
	out, err := exec.Command("ps", "-A", "-o", "pid=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("procwatch: ps -A: %w", err)
	}
	var candidates []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if !strings.Contains(strings.ToLower(fields[1]), "claude") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		candidates = append(candidates, pid)
	}

	// Step 2: check each candidate's cwd via lsof
	var matched []int
	for _, pid := range candidates {
		cwdOut, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-F", "n").Output()
		if err != nil {
			continue
		}
		if strings.Contains(string(cwdOut), dir) {
			matched = append(matched, pid)
		}
	}
	return matched, nil
}
