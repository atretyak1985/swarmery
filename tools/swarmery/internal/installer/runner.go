package installer

import "os/exec"

// Runner abstracts external command execution (launchctl, ps) so that the
// install/uninstall/status logic is testable without touching the real
// launchd domain. Tests substitute a fake; production uses ExecRunner.
type Runner interface {
	// Run executes name with args and returns combined stdout+stderr.
	Run(name string, args ...string) (string, error)
}

// ExecRunner runs commands for real via os/exec.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
