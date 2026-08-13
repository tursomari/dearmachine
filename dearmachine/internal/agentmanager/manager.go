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

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
	"github.com/dearmachine/dearmachine/internal/client"
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
	Prepare(context.Context, string, string) (Launch, error)
	ConsumeStdout(io.Reader, func(string)) (Observation, error)
}

type Launch struct {
	Command       *exec.Cmd
	NativeSession string
}

type Observation struct {
	Reply string
}

type CodexAdapter struct{}

func (CodexAdapter) Name() string       { return "codex" }
func (CodexAdapter) Executable() string { return "codex" }
func (CodexAdapter) Prepare(ctx context.Context, cwd, writableDir string) (Launch, error) {
	command := exec.CommandContext(
		ctx,
		"codex",
		"exec",
		"--skip-git-repo-check",
		"--json",
		"--sandbox",
		"workspace-write",
		"--add-dir",
		writableDir,
		"-",
	)
	command.Dir = cwd
	return Launch{Command: command}, nil
}

func (CodexAdapter) ConsumeStdout(stdout io.Reader, foundSession func(string)) (Observation, error) {
	return consumeCodexStdout(stdout, foundSession)
}

// CodexYoloAdapter runs Codex without approvals or sandboxing. It is a
// separate, explicitly selected backend because its worker inherits every
// host permission available to the DearMachine user.
type CodexYoloAdapter struct{}

func (CodexYoloAdapter) Name() string       { return "codex-yolo" }
func (CodexYoloAdapter) Executable() string { return "codex" }
func (CodexYoloAdapter) Prepare(ctx context.Context, cwd, _ string) (Launch, error) {
	command := exec.CommandContext(
		ctx,
		"codex",
		"exec",
		"--skip-git-repo-check",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"-",
	)
	command.Dir = cwd
	return Launch{Command: command}, nil
}

func (CodexYoloAdapter) ConsumeStdout(stdout io.Reader, foundSession func(string)) (Observation, error) {
	return consumeCodexStdout(stdout, foundSession)
}

func consumeCodexStdout(stdout io.Reader, foundSession func(string)) (Observation, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	observation := Observation{}
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "thread.started" && event.ThreadID != "" {
			foundSession(event.ThreadID)
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" {
			observation.Reply = event.Item.Text
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_, drainErr := io.Copy(io.Discard, stdout)
		return observation, errors.Join(scanErr, drainErr)
	}
	return observation, nil
}

type ForgeAdapter struct {
	newSessionID func() (string, error)
}

func (ForgeAdapter) Name() string       { return "forge" }
func (ForgeAdapter) Executable() string { return "forge" }
func (a ForgeAdapter) Prepare(ctx context.Context, cwd, _ string) (Launch, error) {
	newSessionID := a.newSessionID
	if newSessionID == nil {
		newSessionID = newUUIDv4
	}
	sessionID, err := newSessionID()
	if err != nil {
		return Launch{}, fmt.Errorf("generate forge conversation id: %w", err)
	}
	command := exec.CommandContext(ctx, "forge", "--conversation-id", sessionID)
	command.Dir = cwd
	command.Env = append(os.Environ(),
		"FORGE_UPDATES__FREQUENCY=never",
		"FORGE_UPDATES__AUTO_UPDATE=false",
	)
	return Launch{Command: command, NativeSession: sessionID}, nil
}

func (ForgeAdapter) ConsumeStdout(stdout io.Reader, _ func(string)) (Observation, error) {
	captured := &tailWriter{limit: 64 * 1024}
	_, err := io.Copy(captured, stdout)
	return Observation{Reply: string(captured.content)}, err
}

type tailWriter struct {
	limit   int
	content []byte
}

func (w *tailWriter) Write(content []byte) (int, error) {
	written := len(content)
	if w.limit <= 0 {
		return written, nil
	}
	if len(content) >= w.limit {
		w.content = append(w.content[:0], content[len(content)-w.limit:]...)
		return written, nil
	}
	overflow := len(w.content) + len(content) - w.limit
	if overflow > 0 {
		copy(w.content, w.content[overflow:])
		w.content = w.content[:len(w.content)-overflow]
	}
	w.content = append(w.content, content...)
	return written, nil
}

func newUUIDv4() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

type Manager struct {
	Root             string
	Adapters         map[string]Adapter
	ApprovedBackends []string
	customBackends   []backendcatalog.Backend
	ExecutablePath   func() (string, error)
	LaunchSupervisor func(string) error
	Now              func() time.Time
}

type HealthResult struct {
	Backend string
	Probe   string
	Reply   string
	OK      bool
	Reason  string
}

func New(root string) *Manager {
	m := &Manager{
		Root: filepath.Clean(root),
		Adapters: map[string]Adapter{
			"codex":      CodexAdapter{},
			"codex-yolo": CodexYoloAdapter{},
			"forge":      ForgeAdapter{},
		},
		ExecutablePath: os.Executable,
		Now:            time.Now,
	}
	m.LaunchSupervisor = m.launchSupervisor
	m.customBackends = loadCustomAdapters(m, root)
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

func (m *Manager) SetApprovedBackends(ids []string) error {
	if err := backendcatalog.ValidateIDsWithCustom(ids, m.customBackends, false); err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := m.Adapters[id]; !ok {
			return fmt.Errorf("backend %q has no adapter", id)
		}
	}
	m.ApprovedBackends = append([]string(nil), ids...)
	return nil
}

// DecodeApprovedBackends decodes the backend list from its JSON
// representation and validates every ID against the hardcoded catalog
// plus custom backends loaded from TOML configuration.
func (m *Manager) DecodeApprovedBackends(value string) ([]string, error) {
	return backendcatalog.DecodeWithCustom(value, m.customBackends)
}

func (m *Manager) BackendList() []string {
	return append([]string(nil), m.ApprovedBackends...)
}

func (m *Manager) approvedAdapter(backend string) (Adapter, error) {
	approved := false
	for _, id := range m.ApprovedBackends {
		if id == backend {
			approved = true
			break
		}
	}
	if !approved {
		return nil, fmt.Errorf("backend %q is not approved", backend)
	}
	adapter, ok := m.Adapters[backend]
	if !ok {
		return nil, fmt.Errorf("backend %q has no adapter", backend)
	}
	return adapter, nil
}

func (m *Manager) Send(worker, requestPath, cwd string) (string, error) {
	adapter, err := m.approvedAdapter(worker)
	if err != nil {
		return "", err
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
	launch, err := adapter.Prepare(ctx, meta.CWD, m.TicketDir(id))
	if err != nil {
		_ = m.finish(meta, StatusCrashed)
		return err
	}
	command := launch.Command
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
	meta.NativeSession = launch.NativeSession
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
	_, stdoutErr := adapter.ConsumeStdout(stdout, func(nativeSession string) {
		latest, err := m.readMeta(id)
		if err == nil {
			latest.NativeSession = nativeSession
			meta = latest
			_ = m.writeMeta(latest)
		}
	})
	waitErr := command.Wait()
	<-stderrDone
	latest, readErr := m.readMeta(id)
	if readErr == nil && latest.Status == StatusCancelled {
		return waitErr
	}
	if _, err := os.Stat(filepath.Join(m.TicketDir(id), "ticket-close.md")); err == nil {
		return m.finish(meta, StatusClosed)
	}
	return errors.Join(waitErr, stdoutErr, m.finish(meta, StatusCrashed))
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

func (m *Manager) BackendHealth(ctx context.Context, backend, cwd string) (HealthResult, error) {
	result := HealthResult{Backend: backend}
	adapter, err := m.approvedAdapter(backend)
	if err != nil {
		result.Reason = "not-approved"
		return result, err
	}
	if _, err := exec.LookPath(adapter.Executable()); err != nil {
		result.Reason = "unavailable"
		return result, fmt.Errorf("backend %q is unavailable: %w", backend, err)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		result.Reason = "invalid-cwd"
		return result, fmt.Errorf("resolve health-check working directory: %w", err)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		result.Reason = "invalid-cwd"
		return result, fmt.Errorf("health-check working directory is not accessible: %s", cwd)
	}
	probeFile, err := os.CreateTemp(cwd, ".healthcheck-probe-*.txt")
	if err != nil {
		result.Reason = "probe-path"
		return result, fmt.Errorf("reserve health-check probe path: %w", err)
	}
	probePath := probeFile.Name()
	if err := probeFile.Close(); err != nil {
		_ = os.Remove(probePath)
		result.Reason = "probe-path"
		return result, fmt.Errorf("close health-check probe path: %w", err)
	}
	if err := os.Remove(probePath); err != nil {
		result.Reason = "probe-path"
		return result, fmt.Errorf("prepare health-check probe path: %w", err)
	}
	defer os.Remove(probePath)
	result.Probe = fmt.Sprintf(
		"Write a file at exactly %s containing exactly this line: Dear Machine, backend health probe. Do not modify any other file. Then reply briefly that the probe is complete.",
		probePath,
	)
	launch, err := adapter.Prepare(ctx, cwd, cwd)
	if err != nil {
		result.Reason = "prepare-failed"
		return result, err
	}
	command := launch.Command
	command.Stdin = strings.NewReader(result.Probe)
	stdout, err := command.StdoutPipe()
	if err != nil {
		result.Reason = "stdout-pipe"
		return result, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		result.Reason = "stderr-pipe"
		return result, err
	}
	if err := command.Start(); err != nil {
		result.Reason = "launch-failed"
		return result, err
	}
	stderrCapture := &tailWriter{limit: 64 * 1024}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderrCapture, stderr)
		close(stderrDone)
	}()
	observation, stdoutErr := adapter.ConsumeStdout(stdout, func(string) {})
	waitErr := command.Wait()
	<-stderrDone
	result.Reply = strings.TrimSpace(observation.Reply)
	if ctx.Err() != nil {
		result.Reason = "cancelled"
		return result, ctx.Err()
	}
	if stdoutErr != nil {
		result.Reason = "output-failed"
		return result, stdoutErr
	}
	if waitErr != nil {
		result.Reason = "worker-exit"
		return result, fmt.Errorf("health-check worker failed: %w: %s", waitErr, strings.TrimSpace(string(stderrCapture.content)))
	}
	info, err := os.Stat(probePath)
	if err != nil || !info.Mode().IsRegular() {
		result.Reason = "file-not-written"
		return result, fmt.Errorf("health-check probe file was not written")
	}
	if err := os.Remove(probePath); err != nil {
		result.Reason = "cleanup-failed"
		return result, fmt.Errorf("remove health-check probe file: %w", err)
	}
	result.OK = true
	return result, nil
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

// loadCustomAdapters reads custom-backend definitions from the TOML
// config file and creates ConfigurableAdapter instances.  Returns the
// catalog-compatible Backend list for validation.  Errors are logged
// to stderr; a nil slice indicates no custom backends are available.
func loadCustomAdapters(m *Manager, root string) []backendcatalog.Backend {
	configDir := filepath.Join(filepath.Dir(root), "config")
	configPath := filepath.Join(configDir, "custom-backends.toml")
	custom, err := client.LoadCustomBackends(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-manager: skip custom backends: %v\n", err)
		return nil
	}
	if len(custom) == 0 {
		return nil
	}
	catalogBackends := make([]backendcatalog.Backend, 0, len(custom))
	for _, cb := range custom {
		catalogBackends = append(catalogBackends, cb.Backend)
		if _, exists := m.Adapters[cb.ID]; exists {
			continue
		}
		adapter := NewConfigurableAdapter(
			cb.ID,
			cb.Executable,
			cb.OutputFormat,
			cb.Arguments,
			envSlice(cb.Environment),
		)
		m.Adapters[cb.ID] = adapter
	}
	return catalogBackends
}

func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
