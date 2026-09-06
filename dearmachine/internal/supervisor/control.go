package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

// Status matches the installer SocketDaemonControl v1 schema. PIDs are optional
// diagnostic extensions; they are never authority for signalling a process.
type Status struct {
	Installation  string `json:"installation"`
	Supervisor    string `json:"supervisor"`
	Daemon        string `json:"daemon"`
	Persistence   string `json:"persistence"`
	RetryInMs     *int64 `json:"retryInMs,omitempty"`
	LastExit      string `json:"lastExit,omitempty"`
	SupervisorPID int    `json:"supervisorPid,omitempty"`
	DaemonPID     int    `json:"daemonPid,omitempty"`
}

type Response struct {
	Version int    `json:"version"`
	OK      bool   `json:"ok"`
	Status  Status `json:"status"`
	Error   string `json:"error,omitempty"`
}

type request struct {
	Version   int      `json:"version"`
	Command   string   `json:"command"`
	Argv      []string `json:"argv,omitempty"`
	Directory string   `json:"directory,omitempty"`
}

type operation struct {
	command   string
	argv      []string
	directory string
	reply     chan Response
}

// Config paths are all relative to StateDir. Ready must be a fast, read-only
// check of this child's readiness. Nil means successful exec is sufficient.
type Config struct {
	StateDir       string
	Directory      string
	BeforeStart    func() error
	Command        []string
	Ready          func(int) bool
	Installation   func() string
	Persistence    func() string
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	HealthyRun     time.Duration
	MaxFailures    int
	StartupTimeout time.Duration
	StopTimeout    time.Duration
}

func (c Config) defaults() Config {
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.HealthyRun <= 0 {
		c.HealthyRun = time.Minute
	}
	if c.MaxFailures <= 0 {
		c.MaxFailures = 8
	}
	if c.StartupTimeout <= 0 {
		c.StartupTimeout = 15 * time.Second
	}
	if c.StopTimeout <= 0 {
		c.StopTimeout = 2 * time.Second
	}
	return c
}

func backoff(c Config, failures int) time.Duration {
	delay := c.InitialBackoff
	for i := 1; i < failures && delay < c.MaxBackoff; i++ {
		if delay > c.MaxBackoff/2 {
			return c.MaxBackoff
		}
		delay *= 2
	}
	if delay > c.MaxBackoff {
		return c.MaxBackoff
	}
	return delay
}

// Run holds the singleton lock even while stopped or failed. Cancellation stops
// and reaps the child, closes the control endpoint, and releases ownership.
func Run(ctx context.Context, cfg Config) error {
	runtime.LockOSThread() // Keep Linux parent-death signalling tied to this live thread.
	defer runtime.UnlockOSThread()
	cfg = cfg.defaults()
	if cfg.Directory == "" {
		var err error
		cfg.Directory, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	if len(cfg.Command) == 0 {
		return errors.New("foreground child command is required")
	}
	lock, err := acquire(cfg.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	log, err := openLog(cfg.StateDir)
	if err != nil {
		return err
	}
	defer log.Close()
	path := SocketPath(cfg.StateDir)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("control path is not a socket; inspect state before retrying")
		}
		// Only the flock owner may remove a stale endpoint.
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	requests := make(chan operation)
	serving, cancel := context.WithCancel(context.Background())
	var handlers sync.WaitGroup
	defer func() { cancel(); listener.Close(); handlers.Wait() }()
	handlers.Add(1)
	go func() {
		defer handlers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() { defer handlers.Done(); handle(serving, conn, requests) }()
		}
	}()
	state := Status{Installation: "installed", Supervisor: "stopped", Daemon: "stopped", Persistence: "unknown", SupervisorPID: os.Getpid()}
	var child *childProcess
	var exited <-chan error
	var started, healthySince, retryAt, stopAt time.Time
	failures := 0
	exitReason := ""
	wanted, quitting := true, false
	var pending []operation
	snapshot := func() Status {
		result := state
		if cfg.Persistence != nil {
			result.Persistence = cfg.Persistence()
		}
		if cfg.Installation != nil {
			result.Installation = cfg.Installation()
		}
		if !retryAt.IsZero() {
			ms := max(int64(0), time.Until(retryAt).Milliseconds())
			result.RetryInMs = &ms
		}
		return result
	}
	respond := func(op operation, ok bool, message string) {
		op.reply <- Response{Version: 1, OK: ok, Status: snapshot(), Error: message}
	}
	finish := func(ok bool, message string) {
		for _, op := range pending {
			respond(op, ok, message)
		}
		pending = nil
	}
	failed := func(reason string) {
		state.LastExit = reason
		if !healthySince.IsZero() && time.Since(healthySince) >= cfg.HealthyRun {
			failures = 0
		}
		failures++
		state.Daemon, state.DaemonPID = "stopped", 0
		if failures >= cfg.MaxFailures {
			state.Supervisor = "failed"
			wanted = false
		} else {
			state.Supervisor = "backing-off"
			retryAt = time.Now().Add(backoff(cfg, failures))
		}
		finish(false, "child failed; inspect status and daemon log")
	}
	launch := func() {
		retryAt = time.Time{}
		healthySince = time.Time{}
		exitReason = ""
		var err error
		if cfg.BeforeStart != nil {
			err = cfg.BeforeStart()
		}
		if err == nil {
			child, err = spawnInDirectory(cfg.Command, log, cfg.Directory)
		}
		if err != nil {
			failed("start failed: " + err.Error())
			return
		}
		exited = child.done
		started = time.Now()
		state.Supervisor, state.Daemon, state.DaemonPID = "starting", "unknown", child.cmd.Process.Pid
	}
	stop := func() {
		retryAt = time.Time{}
		if child == nil {
			state.Supervisor, state.Daemon = "stopped", "stopped"
			return
		}
		state.Supervisor = "stopping"
		child.terminate()
		stopAt = time.Now().Add(cfg.StopTimeout)
	}
	launch()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	cancelled := ctx.Done()
	for {
		if quitting && child == nil {
			finish(false, "supervisor exiting")
			return nil
		}
		select {
		case <-cancelled:
			cancelled = nil
			quitting = true
			wanted = false
			finish(false, "supervisor exiting")
			stop()
		case op := <-requests:
			if op.command == "status" {
				respond(op, true, "")
				continue
			}
			if quitting {
				respond(op, false, "supervisor exiting")
				continue
			}
			switch op.command {
			case "down":
				if !wanted && child != nil {
					pending = append(pending, op)
					continue
				}
				finish(false, "startup superseded by stop")
				wanted = false
				stop()
				if child == nil {
					respond(op, true, "")
				} else {
					pending = append(pending, op)
				}
			case "up", "restart":
				changedCommand := len(op.argv) != 0 && !slices.Equal(op.argv, cfg.Command)
				changedDirectory := op.directory != "" && op.directory != cfg.Directory
				if changedCommand || changedDirectory {
					if child != nil || len(pending) != 0 {
						respond(op, false, "launch options differ; run dearmachine down before up with new options")
						continue
					}
					if changedCommand {
						cfg.Command = slices.Clone(op.argv)
					}
					if changedDirectory {
						cfg.Directory = op.directory
					}
				}
				if len(pending) != 0 {
					if op.command == "up" && wanted {
						pending = append(pending, op)
					} else {
						respond(op, false, "lifecycle operation already in progress")
					}
					continue
				}
				if op.command == "up" && state.Supervisor == "running" {
					respond(op, true, "")
					continue
				}
				wanted = true
				pending = append(pending, op)
				if op.command == "restart" {
					stop()
				}
				if child == nil {
					failures = 0
					launch()
				}
			}
		case err := <-exited:
			// Dispose of remaining members of the owned group before another launch.
			child.kill()
			child, exited = nil, nil
			state.Daemon, state.DaemonPID = "stopped", 0
			if state.Supervisor == "stopping" {
				state.Supervisor = "stopped"
				if wanted && !quitting {
					failures = 0
					launch()
				} else {
					finish(true, "")
				}
			} else {
				reason := "exit status 0"
				if err != nil {
					reason = err.Error()
				}
				if exitReason != "" {
					reason = exitReason
				}
				failed(reason)
			}
		case <-ticker.C:
			switch state.Supervisor {
			case "starting":
				// Require observed readiness; a bare exec gets a short settling interval.
				if (cfg.Ready == nil && time.Since(started) >= 20*time.Millisecond) || (cfg.Ready != nil && cfg.Ready(state.DaemonPID)) {
					state.Supervisor, state.Daemon = "running", "running"
					healthySince = time.Now()
					finish(true, "")
				} else if time.Since(started) >= cfg.StartupTimeout {
					exitReason = "readiness timeout"
					child.kill() // The exit event drives the normal bounded retry policy.
				}
			case "stopping":
				if !time.Now().Before(stopAt) {
					child.kill()
				}
			case "backing-off":
				if wanted && !time.Now().Before(retryAt) {
					launch()
				}
			}
		}
	}
}

func handle(ctx context.Context, conn net.Conn, requests chan<- operation) {
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	reader := bufio.NewReaderSize(conn, 65536)
	line, err := reader.ReadSlice('\n')
	var req request
	reply := Response{Version: 1, Error: "invalid control request"}
	if err == nil && json.Unmarshal(line, &req) == nil && req.Version == 1 && validCommand(req.Command) && (len(req.Argv) == 0 || (req.Command == "up" && filepath.IsAbs(req.Argv[0]))) && (req.Directory == "" || (req.Command == "up" && filepath.IsAbs(req.Directory))) {
		op := operation{command: req.Command, argv: req.Argv, directory: req.Directory, reply: make(chan Response, 1)}
		timer := time.NewTimer(3500 * time.Millisecond)
		defer timer.Stop()
		select {
		case requests <- op:
			select {
			case reply = <-op.reply:
			case <-timer.C:
				reply.Error = "operation unconfirmed; inspect status"
			case <-ctx.Done():
				reply.Error = "supervisor exiting"
			}
		case <-timer.C:
			reply.Error = "control busy; inspect status"
		case <-ctx.Done():
			reply.Error = "supervisor exiting"
		}
	}
	_ = json.NewEncoder(conn).Encode(reply)
}

func validCommand(command string) bool {
	return command == "status" || command == "up" || command == "down" || command == "restart"
}

// Request sends exactly one v1 operation, with a total deadline and size bound.
// A transport failure is an unknown outcome, never evidence of stopped state.
func Request(root, command string, timeout time.Duration) (Status, error) {
	return sendRequest(root, request{Version: 1, Command: command}, timeout)
}

// RequestUp is a Go extension that preserves explicit launch options. A running
// child must be stopped before replacing its command; TS needs no argv field.
func RequestUp(root string, argv []string, timeout time.Duration) (Status, error) {
	return sendRequest(root, request{Version: 1, Command: "up", Argv: argv}, timeout)
}

// RequestUpInDirectory preserves relative CLI launch paths when reusing an owner.
func RequestUpInDirectory(root string, argv []string, directory string, timeout time.Duration) (Status, error) {
	return sendRequest(root, request{Version: 1, Command: "up", Argv: argv, Directory: directory}, timeout)
}

func sendRequest(root string, req request, timeout time.Duration) (Status, error) {
	command := req.Command
	if !validCommand(command) || timeout <= 0 {
		return Status{}, errors.New("invalid control request")
	}
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("unix", SocketPath(root), timeout)
	if err != nil {
		return Status{}, fmt.Errorf("supervisor unreachable; inspect dearmachine status: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Status{}, err
	}
	line, err := bufio.NewReaderSize(conn, 65536).ReadSlice('\n')
	if err != nil {
		return Status{}, fmt.Errorf("control outcome unknown; inspect dearmachine status: %w", err)
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return Status{}, err
	}
	if response.Version != 1 || !response.OK {
		if response.Error != "" {
			return response.Status, errors.New(response.Error)
		}
		return response.Status, errors.New("operation unconfirmed; inspect dearmachine status and daemon log")
	}
	if !validStatus(response.Status) {
		return Status{}, errors.New("invalid supervisor status")
	}
	return response.Status, nil
}

func validStatus(s Status) bool {
	contains := func(v string, values ...string) bool {
		for _, item := range values {
			if v == item {
				return true
			}
		}
		return false
	}
	return contains(s.Installation, "absent", "installed", "partial", "unreadable") &&
		contains(s.Supervisor, "starting", "running", "backing-off", "stopping", "stopped", "failed", "unreachable") &&
		contains(s.Daemon, "running", "stopped", "unknown") && contains(s.Persistence, "enabled", "disabled", "unknown") &&
		(s.RetryInMs == nil || *s.RetryInMs >= 0)
}
