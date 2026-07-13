// Package procwatch checks whether the OS processes backing active Claude Code
// sessions are still alive and classifies them as running/orphaned/dead.
package procwatch

// ProcState values stored in sessions.proc_state.
const (
	StateRunning  = "running"
	StateOrphaned = "orphaned"
	StateDead     = "dead"
	StateUnknown  = "unknown"
)

// ProcInfo describes a live process returned by Provider.Info.
type ProcInfo struct {
	PID       int
	StartTime string // lstart string (PID-reuse guard)
	Command   string // process command name
}

// Provider is the OS interface used by Ticker — swappable for tests.
type Provider interface {
	// Info returns nil,nil when the process is gone.
	Info(pid int) (*ProcInfo, error)
	// IsOrphaned returns true when PPID==1 or tty=="??".
	IsOrphaned(pid int) (bool, error)
	// MatchByDir returns PIDs of processes whose command contains "claude"
	// and whose open cwd matches dir. Returns empty slice on no match.
	MatchByDir(dir string) ([]int, error)
}
