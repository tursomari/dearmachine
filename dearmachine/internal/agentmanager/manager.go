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
	StatusOpen                     = "open"
	StatusClosed                   = "closed"
	StatusIncomplete               = "incomplete"
	StatusFailed                   = "failed"
	StatusCrashed                  = "crashed"
	StatusCancelled                = "cancelled"
	CompletionSourceNativeReply    = "native_reply"
	CompletionSourceWorkerArtifact = "worker_artifact"
	maxTicketCloseBytes            = 64 * 1024
)

type Meta struct {
	TicketID         string    `json:"ticket_id"`
	Worker           string    `json:"worker"`
	PID              int       `json:"pid"`
	NativeSession    string    `json:"native_session_id,omitempty"`
	Status           string    `json:"status"`
	CompletionSource string    `json:"completion_source,omitempty"`
	FailureReason    string    `json:"failure_reason,omitempty"`
	StderrTail       string    `json:"stderr_tail,omitempty"`
	ExitCode         *int      `json:"exit_code,omitempty"`
	Signal           string    `json:"signal,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	FinishedAt       time.Time `json:"finished_at,omitempty"`
	CWD              string    `json:"cwd"`
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
	// PromptArgument asks Agent Manager to append the complete request as one
	// positional argument instead of writing it to standard input.
	PromptArgument bool
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

// OMPAdapter runs OMP in its documented noninteractive text mode. Provider,
// model, and reasoning choices remain in OMP's own configuration.
type OMPAdapter struct{}

func (OMPAdapter) Name() string       { return "omp" }
func (OMPAdapter) Executable() string { return "omp" }
func (OMPAdapter) Prepare(ctx context.Context, cwd, _ string) (Launch, error) {
	command := exec.CommandContext(
		ctx,
		"omp",
		"--print",
		"--mode",
		"text",
		"--no-session",
		"--no-pty",
		"--auto-approve",
	)
	command.Dir = cwd
	return Launch{Command: command, PromptArgument: true}, nil
}

func (OMPAdapter) ConsumeStdout(stdout io.Reader, _ func(string)) (Observation, error) {
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
	beforeWriteMeta  func(Meta) error
}

type HealthResult struct {
	Backend string
	Probe   string
	Reply   string
	OK      bool
	Reason  string
}

// ResolvedBackend is an approved backend together with the executable that
// the current process environment will run.  Path is always absolute so a
// launcher can safely derive an explicit service environment from it.
type ResolvedBackend struct {
	ID         string `json:"id"`
	Executable string `json:"executable"`
	Path       string `json:"path"`
}

func New(root string) *Manager {
	m := &Manager{
		Root: filepath.Clean(root),
		Adapters: map[string]Adapter{
			"claude":     ClaudeAdapter{},
			"codex":      CodexAdapter{},
			"codex-yolo": CodexYoloAdapter{},
			"forge":      ForgeAdapter{},
			"omp":        OMPAdapter{},
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

// ConfigureFromDeviceConfig loads the selected backends and their custom
// definitions from one device configuration.  Keeping this here makes Agent
// Manager the authority for the mapping from an approved backend ID to its
// executable, including user-defined adapters.
func (m *Manager) ConfigureFromDeviceConfig(configPath string) error {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve device config path: %w", err)
	}
	custom, err := client.LoadCustomBackends(filepath.Join(filepath.Dir(configPath), "custom-backends.toml"))
	if err != nil {
		return err
	}
	m.setCustomBackends(custom)
	config, err := client.LoadDeviceConfigWithCustom(configPath, m.customBackends...)
	if err != nil {
		return err
	}
	return m.SetApprovedBackends(config.Backends)
}

// ResolveBackends verifies every approved backend against the current PATH
// and returns its concrete executable path in priority order.  It is intended
// for launch-time preflight, before a supervisor snapshots its environment.
func (m *Manager) ResolveBackends() ([]ResolvedBackend, error) {
	resolved := make([]ResolvedBackend, 0, len(m.ApprovedBackends))
	for _, id := range m.ApprovedBackends {
		backend, err := m.ResolveBackend(id)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, backend)
	}
	return resolved, nil
}

// ResolveBackend verifies one approved backend against the current PATH.
func (m *Manager) ResolveBackend(backend string) (ResolvedBackend, error) {
	adapter, err := m.approvedAdapter(backend)
	if err != nil {
		return ResolvedBackend{}, err
	}
	path, err := exec.LookPath(adapter.Executable())
	if err != nil {
		return ResolvedBackend{}, fmt.Errorf("backend %q is unavailable: %w", backend, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return ResolvedBackend{}, fmt.Errorf("resolve backend %q executable path: %w", backend, err)
	}
	return ResolvedBackend{ID: backend, Executable: adapter.Executable(), Path: path}, nil
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
	if _, err := m.ResolveBackend(worker); err != nil {
		return "", err
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
	return []byte(fmt.Sprintf("# Ticket: %s\n# Worker: %s\n# Close-Path: %s\n#\n# Delegated request\n\n%s\n\n# Completion protocol\n# Return a nonempty final response through your backend's native final-answer\n# channel. The Agent Manager owns ticket-close.md and will atomically persist\n# that response when needed. You may instead write the response directly to\n# the Close-Path above. A ticket-close.md write is mandatory control-plane\n# bookkeeping and is explicitly exempt from any read-only, no-changes,\n# reply-only, or do-not-modify-files constraint in the delegated request.\n# Those constraints apply to the task workspace and every other path.\n", id, worker, closePath, request))
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
		err := fmt.Errorf("worker adapter %q is missing", meta.Worker)
		return errors.Join(err, m.fail(meta, "missing_adapter", nil, nil))
	}
	openPath := filepath.Join(m.TicketDir(id), "ticket-open.md")
	request, err := os.ReadFile(openPath)
	if err != nil {
		return errors.Join(err, m.fail(meta, "open_ticket_failed", nil, nil))
	}
	launch, err := adapter.Prepare(ctx, meta.CWD, m.TicketDir(id))
	if err != nil {
		return errors.Join(err, m.fail(meta, "prepare_failed", nil, nil))
	}
	command := launch.Command
	if command == nil {
		err := errors.New("adapter returned a nil command")
		return errors.Join(err, m.fail(meta, "prepare_failed", nil, nil))
	}
	configureLaunchInput(&launch, request)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return errors.Join(err, m.fail(meta, "stdout_pipe_failed", nil, nil))
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return errors.Join(err, m.fail(meta, "stderr_pipe_failed", nil, nil))
	}
	if err := command.Start(); err != nil {
		return errors.Join(err, m.fail(meta, "start_failed", nil, nil))
	}
	meta.PID = command.Process.Pid
	meta.NativeSession = launch.NativeSession
	meta.StartedAt = m.Now()
	stderrCapture := &tailWriter{limit: 64 * 1024}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderrCapture, stderr)
		close(stderrDone)
	}()
	if err := m.writeMeta(meta); err != nil {
		_ = command.Process.Kill()
		_, stdoutErr := io.Copy(io.Discard, stdout)
		<-stderrDone
		waitErr := command.Wait()
		return errors.Join(err, stdoutErr, waitErr, m.failAfterExit(meta, StatusFailed, "metadata_write_failed", stderrCapture.content, waitErr))
	}
	observation, consumeErr := adapter.ConsumeStdout(stdout, func(nativeSession string) {
		latest, err := m.readMeta(id)
		if err == nil {
			latest.NativeSession = nativeSession
			meta = latest
			_ = m.writeMeta(latest)
		}
	})
	_, drainErr := io.Copy(io.Discard, stdout)
	stdoutErr := errors.Join(consumeErr, drainErr)
	<-stderrDone
	waitErr := command.Wait()
	latest, readErr := m.readMeta(id)
	if readErr == nil && latest.Status == StatusCancelled {
		latest.FailureReason = "cancelled"
		latest.StderrTail = string(stderrCapture.content)
		recordProcessFailure(&latest, waitErr)
		return errors.Join(waitErr, m.writeMeta(latest))
	}
	if readErr == nil {
		meta = latest
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), m.failAfterExit(meta, StatusCancelled, "context_cancelled", stderrCapture.content, waitErr))
	}
	if waitErr != nil {
		reason := "worker_exit"
		status := StatusFailed
		if processWasSignaled(waitErr) {
			reason = "worker_signal"
			status = StatusCrashed
		}
		return errors.Join(waitErr, stdoutErr, m.failAfterExit(meta, status, reason, stderrCapture.content, waitErr))
	}
	if stdoutErr != nil {
		return errors.Join(stdoutErr, m.failAfterExit(meta, StatusFailed, "output_failed", stderrCapture.content, nil))
	}
	closePath := filepath.Join(m.TicketDir(id), "ticket-close.md")
	if err := secureCloseArtifact(closePath); err == nil {
		meta.CompletionSource = CompletionSourceWorkerArtifact
		return m.finish(meta, StatusClosed)
	} else if !os.IsNotExist(err) {
		return errors.Join(err, m.failAfterExit(meta, StatusFailed, "close_artifact_unreadable", stderrCapture.content, nil))
	}
	if strings.TrimSpace(observation.Reply) == "" {
		return m.failAfterExit(meta, StatusIncomplete, "empty_reply", stderrCapture.content, nil)
	}
	if err := publishReply(m.TicketDir(id), observation.Reply); err != nil {
		if errors.Is(err, os.ErrExist) && secureCloseArtifact(closePath) == nil {
			meta.CompletionSource = CompletionSourceWorkerArtifact
			return m.finish(meta, StatusClosed)
		}
		return errors.Join(err, m.failAfterExit(meta, StatusFailed, "publish_failed", stderrCapture.content, nil))
	}
	if err := secureCloseArtifact(closePath); err != nil {
		return errors.Join(err, m.failAfterExit(meta, StatusFailed, "publish_failed", stderrCapture.content, nil))
	}
	meta.CompletionSource = CompletionSourceNativeReply
	return m.finish(meta, StatusClosed)
}

func configureLaunchInput(launch *Launch, request []byte) {
	if launch.PromptArgument {
		launch.Command.Args = append(launch.Command.Args, string(request))
		return
	}
	launch.Command.Stdin = strings.NewReader(string(request))
}

func secureCloseArtifact(path string) error {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !linkInfo.Mode().IsRegular() {
		return fmt.Errorf("close artifact is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("close artifact is not a regular file")
	}
	if info.Size() > maxTicketCloseBytes {
		return fmt.Errorf("close artifact exceeds %d-byte limit", maxTicketCloseBytes)
	}
	if info.Mode().Perm() != 0o600 {
		if err := file.Chmod(0o600); err != nil {
			return fmt.Errorf("set private close artifact mode: %w", err)
		}
	}
	return nil
}

func publishReply(ticketDir, reply string) error {
	reply = strings.TrimSpace(reply)
	content := []byte(reply)
	if len(content) > maxTicketCloseBytes {
		content = content[:maxTicketCloseBytes]
	}
	temporary, err := os.CreateTemp(ticketDir, ".ticket-close-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Link(temporaryPath, filepath.Join(ticketDir, "ticket-close.md"))
}

func (m *Manager) Status(id string) (Meta, error) {
	return m.readMeta(id)
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
	if err := m.finish(meta, StatusCancelled); err != nil {
		return err
	}
	if meta.PID != 0 && processAlive(meta.PID) {
		if err := syscall.Kill(meta.PID, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("cancel worker: %w", err)
		}
	}
	return nil
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
	if _, err := m.ResolveBackend(backend); err != nil {
		result.Reason = "unavailable"
		return result, err
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
	configureLaunchInput(&launch, []byte(result.Probe))
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
	<-stderrDone
	waitErr := command.Wait()
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
	if m.beforeWriteMeta != nil {
		if err := m.beforeWriteMeta(meta); err != nil {
			return fmt.Errorf("write ticket metadata: %w", err)
		}
	}
	content, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	// Status readers and the supervisor can run in separate processes. Publish a
	// complete private file so cancellation never exposes truncated JSON.
	temporary, err := os.CreateTemp(m.TicketDir(meta.TicketID), ".meta-*.tmp")
	if err != nil {
		return fmt.Errorf("create ticket metadata: %w", err)
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(append(content, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write ticket metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close ticket metadata: %w", err)
	}
	if err := os.Rename(temporary.Name(), filepath.Join(m.TicketDir(meta.TicketID), "meta.json")); err != nil {
		return fmt.Errorf("publish ticket metadata: %w", err)
	}
	return nil
}

func (m *Manager) finish(meta Meta, status string) error {
	if status != StatusCancelled {
		latest, err := m.readMeta(meta.TicketID)
		if err == nil {
			if latest.Status == StatusCancelled {
				return nil
			}
		}
	}
	meta.Status = status
	meta.FinishedAt = m.Now()
	return m.writeMeta(meta)
}

func (m *Manager) fail(meta Meta, reason string, stderr []byte, processErr error) error {
	meta.FailureReason = reason
	meta.StderrTail = string(stderr)
	meta.ExitCode = nil
	meta.Signal = ""
	recordProcessFailure(&meta, processErr)
	return m.finish(meta, StatusFailed)
}

func (m *Manager) failAfterExit(meta Meta, status, reason string, stderr []byte, processErr error) error {
	if processErr == nil {
		exitCode := 0
		meta.ExitCode = &exitCode
	}
	meta.FailureReason = reason
	meta.StderrTail = string(stderr)
	meta.Signal = ""
	if processErr != nil {
		meta.ExitCode = nil
		recordProcessFailure(&meta, processErr)
	}
	return m.finish(meta, status)
}

func processWasSignaled(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}

func recordProcessFailure(meta *Meta, err error) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if ok && status.Signaled() {
		meta.Signal = status.Signal().String()
		return
	}
	exitCode := exitErr.ExitCode()
	if exitCode >= 0 {
		meta.ExitCode = &exitCode
	}
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
	m.setCustomBackends(custom)
	return append([]backendcatalog.Backend(nil), m.customBackends...)
}

func (m *Manager) setCustomBackends(custom []client.CustomBackend) {
	for _, backend := range m.customBackends {
		if _, builtIn := backendcatalog.Lookup(backend.ID); !builtIn {
			delete(m.Adapters, backend.ID)
		}
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
	m.customBackends = catalogBackends
}

func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
