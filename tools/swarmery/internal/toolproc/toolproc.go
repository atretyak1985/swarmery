// Package toolproc owns long-lived tool processes (serena MCP servers) on
// behalf of the daemon — at most one per project. Each child runs in its own
// process group (Setpgid) so Stop can SIGTERM/SIGKILL the whole tree, not just
// the launcher. Lifecycle is stopped → starting → running|failed: the manager
// scans merged stdout+stderr for the dashboard URL to flip starting→running,
// falls back to probing serena's default dashboard ports, and keeps a bounded
// log tail for the UI. The launch command is injectable (Config.Command) so
// tests substitute a stub script for the real serena binary.
package toolproc

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// State is the lifecycle phase of one managed process.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateFailed   State = "failed"
)

var (
	// ErrAlreadyRunning — Start refused because the project already has a
	// process in starting/running state.
	ErrAlreadyRunning = errors.New("tool process already running for project")
	// ErrNotRunning — Stop found nothing alive for the project.
	ErrNotRunning = errors.New("no running tool process for project")
)

// Timing knobs are package vars (not consts) so tests can shrink them; every
// Start captures its own copies, so mutating them never races with live procs.
var (
	// urlWait is how long stdout parsing gets before the port probe kicks in.
	urlWait = 20 * time.Second
	// killWait is the SIGTERM grace period before Stop escalates to SIGKILL.
	killWait = 5 * time.Second
	// probePorts are serena's default dashboard ports, tried in order.
	probePorts = []int{24282, 24283, 24284, 24285, 24286, 24287, 24288, 24289, 24290}
	// probeTimeout bounds each individual dashboard HTTP probe.
	probeTimeout = 1 * time.Second
)

// logTailCap bounds the per-process log ring buffer.
const logTailCap = 40

// dashboardURLRe extracts the URL from serena's "dashboard started at …" line.
// The host part accepts hostnames as well as IPv4 (serena may print
// "http://localhost:24282/…").
var dashboardURLRe = regexp.MustCompile(`https?://[0-9A-Za-z.-]+:[0-9]+[^\s]*`)

// Status is a point-in-time snapshot safe to hand to API handlers.
type Status struct {
	State        State
	DashboardURL string // e.g. "http://127.0.0.1:24282/dashboard/index.html"; "" until parsed
	StartedAt    time.Time
	LogTail      []string // last ≤40 stdout+stderr lines
	Err          string   // set when State==StateFailed
}

// Config parameterizes a Manager.
type Config struct {
	// Command builds the argv to launch the tool for a project dir. Injectable
	// for tests; nil means DefaultCommand.
	Command func(projectDir string, mcpPort int) (bin string, args []string)
}

// DefaultCommand is the production serena launch: SSE transport on a
// daemon-chosen free port, web dashboard on but never auto-opening a browser.
func DefaultCommand(projectDir string, mcpPort int) (string, []string) {
	return "serena", []string{
		"start-mcp-server",
		"--project", projectDir,
		"--transport", "sse",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(mcpPort),
		"--enable-web-dashboard", "true",
		"--open-web-dashboard", "false",
	}
}

// proc is one managed child. All mutable fields are guarded by Manager.mu.
type proc struct {
	cmd          *exec.Cmd
	pgid         int
	state        State
	dashboardURL string
	startedAt    time.Time
	logTail      []string
	errMsg       string
	stopping     bool          // Stop in flight: exit means stopped, not failed
	done         chan struct{} // closed once the child is reaped

	// Knob copies captured at Start so later knob mutation cannot race.
	urlWait      time.Duration
	killWait     time.Duration
	probePorts   []int
	probeTimeout time.Duration
}

// Manager starts, tracks, and stops one tool process per project ID.
type Manager struct {
	cfg   Config
	mu    sync.Mutex
	procs map[int64]*proc
}

func NewManager(cfg Config) *Manager {
	if cfg.Command == nil {
		cfg.Command = DefaultCommand
	}
	return &Manager{cfg: cfg, procs: make(map[int64]*proc)}
}

// Start launches the tool for projectID rooted at projectDir. A stopped or
// failed entry is replaced; a starting/running one yields ErrAlreadyRunning.
// The MCP port is a fresh ephemeral port reserved-then-released just before
// exec (the tiny reuse window is acceptable on loopback).
func (m *Manager) Start(projectID int64, projectDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if p := m.procs[projectID]; p != nil && (p.state == StateStarting || p.state == StateRunning) {
		return ErrAlreadyRunning
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("reserve mcp port: %w", err)
	}
	mcpPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	bin, args := m.cfg.Command(projectDir, mcpPort)
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Merge stdout+stderr through one pipe; being *os.File keeps cmd.Wait
	// free of copy goroutines, so the reaper below controls all ordering.
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("stdio pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("start %s: %w", bin, err)
	}
	pw.Close() // child holds the write end now

	p := &proc{
		cmd:          cmd,
		pgid:         cmd.Process.Pid, // Setpgid with Pgid 0 → pgid == child pid
		state:        StateStarting,
		startedAt:    time.Now(),
		done:         make(chan struct{}),
		urlWait:      urlWait,
		killWait:     killWait,
		probePorts:   append([]int(nil), probePorts...),
		probeTimeout: probeTimeout,
	}
	m.procs[projectID] = p

	go m.readAndReap(p, pr)
	go m.probeFallback(p)
	return nil
}

// readAndReap drains the merged output line-by-line into the ring buffer,
// promoting starting→running on the first dashboard URL line, then reaps the
// child once the pipe hits EOF. EOF-before-Wait ordering guarantees the log
// tail (incl. the last line quoted in Err) is complete when state flips.
func (m *Manager) readAndReap(p *proc, pr *os.File) {
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		m.mu.Lock()
		p.logTail = append(p.logTail, line)
		if len(p.logTail) > logTailCap {
			p.logTail = p.logTail[len(p.logTail)-logTailCap:]
		}
		if p.state == StateStarting && strings.Contains(strings.ToLower(line), "dashboard") {
			if url := dashboardURLRe.FindString(line); url != "" {
				p.dashboardURL = url
				p.state = StateRunning
			}
		}
		m.mu.Unlock()
	}
	if scanErr := sc.Err(); scanErr != nil {
		// The scan loop ended without EOF (e.g. a line beyond the buffer cap).
		// Record it, drop the read end, and SIGKILL the whole group before
		// Wait — otherwise a live child writing to the broken pipe could keep
		// Wait blocked forever and the reaper (and Stop) would hang.
		m.mu.Lock()
		p.logTail = append(p.logTail, "log scanner error: "+scanErr.Error())
		if len(p.logTail) > logTailCap {
			p.logTail = p.logTail[len(p.logTail)-logTailCap:]
		}
		m.mu.Unlock()
		pr.Close()
		syscall.Kill(-p.pgid, syscall.SIGKILL) //nolint:errcheck // group may already be gone
	} else {
		pr.Close()
	}

	waitErr := p.cmd.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case p.stopping:
		p.state = StateStopped
	case p.state == StateStarting || p.state == StateRunning:
		// Died on its own while it was supposed to be serving.
		p.state = StateFailed
		reason := "process exited"
		if waitErr != nil {
			reason = waitErr.Error()
		}
		if n := len(p.logTail); n > 0 {
			reason += ": " + p.logTail[n-1]
		}
		p.errMsg = reason
	}
	close(p.done)
}

// probeFallback covers serena builds that never print the dashboard URL: after
// urlWait, if stdout parsing has not promoted the proc, probe the default
// dashboard ports once and accept the first HTTP 200. No hit → stay starting
// (the UI still has the log tail).
func (m *Manager) probeFallback(p *proc) {
	select {
	case <-p.done:
		return
	case <-time.After(p.urlWait):
	}
	m.mu.Lock()
	starting := p.state == StateStarting
	m.mu.Unlock()
	if !starting {
		return
	}

	client := &http.Client{Timeout: p.probeTimeout}
	for _, port := range p.probePorts {
		url := fmt.Sprintf("http://127.0.0.1:%d/dashboard/index.html", port)
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		m.mu.Lock()
		// The stopping guard keeps a late probe hit from flipping
		// starting→running while a Stop is already in flight.
		if p.state == StateStarting && !p.stopping {
			p.dashboardURL = url
			p.state = StateRunning
		}
		m.mu.Unlock()
		return
	}
}

// Stop SIGTERMs the whole process group, escalating to SIGKILL after killWait.
// It returns once the child is reaped and the state is stopped.
func (m *Manager) Stop(projectID int64) error {
	m.mu.Lock()
	p := m.procs[projectID]
	if p == nil || (p.state != StateStarting && p.state != StateRunning) {
		m.mu.Unlock()
		return ErrNotRunning
	}
	p.stopping = true
	pgid, done, grace := p.pgid, p.done, p.killWait
	m.mu.Unlock()

	syscall.Kill(-pgid, syscall.SIGTERM) //nolint:errcheck // group may already be gone

	select {
	case <-done:
	case <-time.After(grace):
		syscall.Kill(-pgid, syscall.SIGKILL) //nolint:errcheck // best-effort escalation
		<-done                               // SIGKILL is not ignorable; the reaper will close done
	}

	// readAndReap committed StateStopped (stopping was set) before closing done.
	return nil
}

// StopAll stops every managed process in parallel — the daemon-shutdown hook.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.procs))
	for id := range m.procs {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			m.Stop(id) //nolint:errcheck // already-stopped entries are fine
		}(id)
	}
	wg.Wait()
}

// Status snapshots the project's process; unknown projects read as stopped.
func (m *Manager) Status(projectID int64) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[projectID]
	if p == nil {
		return Status{State: StateStopped}
	}
	return Status{
		State:        p.state,
		DashboardURL: p.dashboardURL,
		StartedAt:    p.startedAt,
		LogTail:      append([]string(nil), p.logTail...),
		Err:          p.errMsg,
	}
}
