package procwatch

import "strings"

// FakeProcess is one process entry in FakeProvider.
type FakeProcess struct {
	PID       int
	StartTime string
	Command   string
	Orphaned  bool
	CWD       string
}

// FakeProvider is a deterministic Provider for unit tests.
type FakeProvider struct {
	Procs []FakeProcess
}

func (f *FakeProvider) Info(pid int) (*ProcInfo, error) {
	for _, p := range f.Procs {
		if p.PID == pid {
			return &ProcInfo{PID: p.PID, StartTime: p.StartTime, Command: p.Command}, nil
		}
	}
	return nil, nil // gone
}

func (f *FakeProvider) IsOrphaned(pid int) (bool, error) {
	for _, p := range f.Procs {
		if p.PID == pid {
			return p.Orphaned, nil
		}
	}
	return false, nil
}

func (f *FakeProvider) MatchByDir(dir string) ([]int, error) {
	var pids []int
	for _, p := range f.Procs {
		if p.CWD == dir && strings.Contains(strings.ToLower(p.Command), "claude") {
			pids = append(pids, p.PID)
		}
	}
	return pids, nil
}
