package agentmanager

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	StatusOpen      = "open"
	StatusClosed    = "closed"
	StatusCrashed   = "crashed"
	StatusCancelled = "cancelled"
)

type Meta struct {
	TicketID      string    `json:"ticket_id"`
	Worker        string    `json:"worker"`
	PID           int       `json:"pid"`
	NativeSession string    `json:"native_session_id,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	CWD           string    `json:"cwd"`
}

type Adapter interface {
	Name() string
	Executable() string
	Command(context.Context, string) *exec.Cmd
}

type CodexAdapter struct{}

func (CodexAdapter) Name() string       { return "codex" }
func (CodexAdapter) Executable() string { return "codex" }
func (CodexAdapter) Command(ctx context.Context, cwd string) *exec.Cmd {
	command := exec.CommandContext(ctx, "codex", "exec", "--json", "-")
	command.Dir = cwd
	return command
}

type Manager struct {
	Root             string
	Adapters         map[string]Adapter
	ExecutablePath   func() (string, error)
	LaunchSupervisor func(string) error
	Now              func() time.Time
}

func New(root string) *Manager {
	m := &Manager{
		Root:           filepath.Clean(root),
		Adapters:       map[string]Adapter{"codex": CodexAdapter{}},
		ExecutablePath: os.Executable,
		Now:            time.Now,
	}
	m.LaunchSupervisor = m.launchSupervisor
	return m
}

func DefaultRoot() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("DEARMACHINE_HOME")); configured != "" {
		return filepath.Join(configured, "agent-manager"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".dearmachine", "agent-manager"), nil
}

func (m *Manager) TicketDir(id string) string { return filepath.Join(m.Root, "tickets", id) }

func (m *Manager) Send(worker, requestPath, cwd string) (string, error) {
	adapter, ok := m.Adapters[worker]
	if !ok {
		return "", fmt.Errorf("unknown worker %q", worker)
	}
	if _, err := exec.LookPath(adapter.Executable()); err != nil {
		return "", fmt.Errorf("worker %q is unavailable: %w", worker, err)
	}
	request, err := os.ReadFile(requestPath)
	if err != nil {
		return "", fmt.Errorf("read work request: %w", err)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return "", fmt.Errorf("working directory is not accessible: %s", cwd)
	}
	id, err := newTicketID(m.Now())
	if err != nil {
		return "", err
	}
	directory := m.TicketDir(id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create ticket directory: %w", err)
	}
	closePath := filepath.Join(directory, "ticket-close.md")
	content := ticketContent(id, worker, closePath, request)
	if err := os.WriteFile(filepath.Join(directory, "ticket-open.md"), content, 0o600); err != nil {
		return "", fmt.Errorf("write open ticket: %w", err)
	}
	meta := Meta{TicketID: id, Worker: worker, Status: StatusOpen, CreatedAt: m.Now(), CWD: cwd}
	if err := m.writeMeta(meta); err != nil {
		return "", err
	}
	if err := m.LaunchSupervisor(id); err != nil {
		return "", fmt.Errorf("launch worker supervisor: %w", err)
	}
	return id, nil
}

func ticketContent(id, worker, closePath string, request []byte) []byte {
	return []byte(fmt.Sprintf("# Ticket: %s\n# Worker: %s\n# Close-Path: %s\n#\n# You have been delegated to complete the following work. Do your work\n# in the repository, then write your response (what you did, what changed,\n# any issues) to the Close-Path above. You do not need to run any other\n# commands or close any tickets. Just save your response to that path.\n\n%s", id, worker, closePath, request))
}

func newTicketID(now time.Time) (string, error) {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate ticket id: %w", err)
	}
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(random[:]), nil
}

func (m *Manager) launchSupervisor(id string) error {
	path, err := m.ExecutablePath()
	if err != nil {
		return err
	}
	command := exec.Command(path, "_supervise", id)
	command.Env = append(os.Environ(), "DEARMACHINE_HOME="+filepath.Dir(m.Root))
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command.Start()
}

func (m *Manager) Supervise(ctx context.Context, id string) error {
	meta, err := m.readMeta(id)
	if err != nil {
		return err
	}
	adapter, ok := m.Adapters[meta.Worker]
	if !ok {
		return m.finish(meta, StatusCrashed)
	}
	openPath := filepath.Join(m.TicketDir(id), "ticket-open.md")
	request, err := os.ReadFile(openPath)
	if err != nil {
		_ = m.finish(meta, StatusCrashed)
		return err
	}
	command := adapter.Command(ctx, meta.CWD)
	command.Stdin = strings.NewReader(string(request))
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = m.finish(meta, StatusCrashed)
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = m.finish(meta, StatusCrashed)
		return err
	}
	if err := command.Start(); err != nil {
		_ = m.finish(meta, StatusCrashed)
		return err
	}
	meta.PID = command.Process.Pid
	meta.StartedAt = m.Now()
	if err := m.writeMeta(meta); err != nil {
		_ = command.Process.Kill()
		return err
	}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "thread.started" && event.ThreadID != "" {
			latest, err := m.readMeta(id)
			if err == nil {
				latest.NativeSession = event.ThreadID
				meta = latest
				_ = m.writeMeta(latest)
			}
		}
	}
	waitErr := command.Wait()
	<-stderrDone
	latest, readErr := m.readMeta(id)
	if readErr == nil && latest.Status == StatusCancelled {
		return waitErr
	}
	if _, err := os.Stat(filepath.Join(m.TicketDir(id), "ticket-close.md")); err == nil {
		return m.finish(meta, StatusClosed)
	}
	return errors.Join(waitErr, scanner.Err(), m.finish(meta, StatusCrashed))
}

func (m *Manager) Status(id string) (Meta, error) {
	meta, err := m.readMeta(id)
	if err != nil {
		return Meta{}, err
	}
	if meta.Status == StatusOpen {
		if _, err := os.Stat(filepath.Join(m.TicketDir(id), "ticket-close.md")); err == nil {
			meta.Status = StatusClosed
		} else if meta.PID != 0 && !processAlive(meta.PID) {
			meta.Status = StatusCrashed
		}
	}
	return meta, nil
}

func (m *Manager) View(id string, output io.Writer) error {
	open, err := os.ReadFile(filepath.Join(m.TicketDir(id), "ticket-open.md"))
	if err != nil {
		return fmt.Errorf("read open ticket: %w", err)
	}
	fmt.Fprintf(output, "--- ticket-open.md ---\n%s", open)
	closeContent, err := os.ReadFile(filepath.Join(m.TicketDir(id), "ticket-close.md"))
	if err == nil {
		fmt.Fprintf(output, "\n--- ticket-close.md ---\n%s", closeContent)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read close ticket: %w", err)
	}
	return nil
}

func (m *Manager) Cancel(id string) error {
	meta, err := m.readMeta(id)
	if err != nil {
		return err
	}
	if meta.Status != StatusOpen {
		return nil
	}
	if meta.PID != 0 && processAlive(meta.PID) {
		if err := syscall.Kill(meta.PID, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("cancel worker: %w", err)
		}
	}
	return m.finish(meta, StatusCancelled)
}

func (m *Manager) CancelAll() error {
	metas, err := m.List("")
	if err != nil {
		return err
	}
	var result error
	for _, meta := range metas {
		if meta.Status == StatusOpen {
			result = errors.Join(result, m.Cancel(meta.TicketID))
		}
	}
	return result
}

func (m *Manager) WorkerHealth(worker string) (string, error) {
	adapter, ok := m.Adapters[worker]
	if !ok {
		return "", fmt.Errorf("unknown worker %q", worker)
	}
	path, err := exec.LookPath(adapter.Executable())
	if err != nil {
		return "", fmt.Errorf("%s unavailable: %w", worker, err)
	}
	return path, nil
}

func (m *Manager) List(worker string) ([]Meta, error) {
	directories, err := os.ReadDir(filepath.Join(m.Root, "tickets"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	metas := make([]Meta, 0, len(directories))
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		meta, err := m.Status(directory.Name())
		if err == nil && (worker == "" || meta.Worker == worker) {
			metas = append(metas, meta)
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].CreatedAt.Before(metas[j].CreatedAt) })
	return metas, nil
}

func (m *Manager) readMeta(id string) (Meta, error) {
	content, err := os.ReadFile(filepath.Join(m.TicketDir(id), "meta.json"))
	if err != nil {
		return Meta{}, fmt.Errorf("read ticket metadata: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(content, &meta); err != nil {
		return Meta{}, fmt.Errorf("parse ticket metadata: %w", err)
	}
	return meta, nil
}

func (m *Manager) writeMeta(meta Meta) error {
	content, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.TicketDir(meta.TicketID), "meta.json"), append(content, '\n'), 0o600); err != nil {
		return fmt.Errorf("write ticket metadata: %w", err)
	}
	return nil
}

func (m *Manager) finish(meta Meta, status string) error {
	meta.Status = status
	meta.FinishedAt = m.Now()
	return m.writeMeta(meta)
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
