package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
	"github.com/dearmachine/dearmachine/internal/machtianiconfig"
)

type ResultKind string

const (
	ResultAnswer   ResultKind = "answer"
	ResultQuestion ResultKind = "question"
)

type RunResult struct {
	Kind               ResultKind
	Text               string
	MagnificaHumanitas *MagnificaHumanitas
}

type MagnificaHumanitas struct {
	Paragraph int    `json:"paragraph"`
	Line      int    `json:"line"`
	Quote     string `json:"quote"`
}

type TurnContext struct {
	InboxPath    string
	OutboxPath   string
	ManifestPath string
	Tier         string
}

type AgentRunner struct {
	binary             string
	projectDir         string
	model              string
	backends           []string
	customBackends     []backendcatalog.Backend
	manager            string
	magnificaHumanitas bool
	invoke             func(*exec.Cmd) error
	activeMu           sync.Mutex
	active             map[string]*activeCommand
	interrupt          func(*exec.Cmd) error
}

type activeCommand struct {
	command *exec.Cmd
	stopped bool
}

var ErrGracefullyStopped = errors.New("machtiani run stopped gracefully")

func (r *AgentRunner) SetMagnificaHumanitas(enabled bool) {
	r.magnificaHumanitas = enabled
}

func (r *AgentRunner) ConfigureAgentManaged(backends []string, managerPath string, customCatalog []backendcatalog.Backend) error {
	if err := backendcatalog.ValidateIDsWithCustom(backends, customCatalog, false); err != nil {
		return fmt.Errorf("validate agent backends: %w", err)
	}
	if strings.TrimSpace(managerPath) == "" || !filepath.IsAbs(managerPath) {
		return fmt.Errorf("absolute agent-manager path is required")
	}
	r.backends = append([]string(nil), backends...)
	r.customBackends = append([]backendcatalog.Backend(nil), customCatalog...)
	r.manager = filepath.Clean(managerPath)
	return nil
}

type sessionState struct {
	Status             string              `json:"status"`
	Goal               string              `json:"goal"`
	SuspendedUserInput *suspendedUserInput `json:"suspended_user_input"`
	MagnificaHumanitas *MagnificaHumanitas `json:"magnifica_humanitas,omitempty"`
}

type suspendedUserInput struct {
	Question string `json:"question"`
	Context  string `json:"context"`
}

func NewAgentRunner(binary, projectDir, model string) (*AgentRunner, error) {
	if strings.TrimSpace(binary) == "" {
		return nil, fmt.Errorf("machtiani binary is required")
	}
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("agent project directory is required")
	}
	return &AgentRunner{
		binary:     binary,
		projectDir: projectDir,
		model:      strings.TrimSpace(model),
		active:     make(map[string]*activeCommand),
	}, nil
}

// Stop asks the current run for threadID to stop at its graceful interrupt
// boundary. It returns false when no command remains active for that thread.
func (r *AgentRunner) Stop(threadID string) bool {
	r.activeMu.Lock()
	active := r.active[threadID]
	if active == nil {
		r.activeMu.Unlock()
		return false
	}
	active.stopped = true
	command := active.command
	r.activeMu.Unlock()
	_ = command.Cancel()
	return true
}

func (r *AgentRunner) registerActive(threadID string, command *exec.Cmd) {
	r.activeMu.Lock()
	r.active[threadID] = &activeCommand{command: command}
	r.activeMu.Unlock()
}

func (r *AgentRunner) releaseActive(threadID string, command *exec.Cmd) bool {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	active := r.active[threadID]
	if active == nil || active.command != command {
		return false
	}
	delete(r.active, threadID)
	return active.stopped
}

func (r *AgentRunner) cancel(command *exec.Cmd) error {
	if r.interrupt != nil {
		return r.interrupt(command)
	}
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return command.Process.Signal(os.Interrupt)
}

func (r *AgentRunner) Sync(ctx context.Context) error {
	args, err := machtianiconfig.SyncArgs(r.model, false)
	if err != nil {
		return fmt.Errorf("resolve sync model: %w", err)
	}
	command := exec.CommandContext(ctx, r.binary, args...)
	command.Dir = r.projectDir
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := r.runCommand(command); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf(
			"machtiani sync failed: %w: %s",
			err,
			strings.TrimSpace(output.String()),
		)
	}
	return nil
}

func (r *AgentRunner) ValidateTurnContext(tc TurnContext) error {
	if err := validateTurnContextPath("inbox path", tc.InboxPath); err != nil {
		return err
	}
	if err := validateTurnContextPath("outbox path", tc.OutboxPath); err != nil {
		return err
	}
	if err := validateTurnContextPath("manifest path", tc.ManifestPath); err != nil {
		return err
	}
	if strings.TrimSpace(tc.Tier) == "" {
		return fmt.Errorf("turn context tier is required")
	}
	switch tc.Tier {
	case "plain", "formatted", "complete":
		return nil
	default:
		return fmt.Errorf("turn context tier %q is not supported", tc.Tier)
	}
}

func validateTurnContextPath(label, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("turn context %s is required", label)
	}
	if !filepath.IsAbs(filepath.Clean(path)) {
		return fmt.Errorf("turn context %s must be absolute", label)
	}
	return nil
}

func (r *AgentRunner) Run(
	ctx context.Context,
	session Session,
	text,
	finalPath string,
	tc TurnContext,
) (RunResult, error) {
	return r.runObserved(ctx, session, text, finalPath, tc, nil)
}

func (r *AgentRunner) runObserved(
	ctx context.Context,
	session Session,
	text,
	finalPath string,
	tc TurnContext,
	started func(),
) (RunResult, error) {
	if err := r.validateRun(ctx, finalPath, tc); err != nil {
		return RunResult{}, err
	}
	return r.run(ctx, session, &text, finalPath, tc, started)
}

func (r *AgentRunner) RunResume(
	ctx context.Context,
	session Session,
	finalPath string,
	tc TurnContext,
) (RunResult, error) {
	return r.runResumeObserved(ctx, session, finalPath, tc, nil)
}

func (r *AgentRunner) runResumeObserved(
	ctx context.Context,
	session Session,
	finalPath string,
	tc TurnContext,
	started func(),
) (RunResult, error) {
	if err := r.validateRun(ctx, finalPath, tc); err != nil {
		return RunResult{}, err
	}
	return r.run(ctx, session, nil, finalPath, tc, started)
}

func (r *AgentRunner) validateRun(ctx context.Context, finalPath string, tc TurnContext) error {
	if err := r.ValidateTurnContext(tc); err != nil {
		return fmt.Errorf("validate turn context: %w", err)
	}
	if err := r.Sync(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(finalPath) == "" {
		return fmt.Errorf("agent final answer path is required")
	}
	return nil
}

func (r *AgentRunner) run(
	ctx context.Context,
	session Session,
	prompt *string,
	finalPath string,
	tc TurnContext,
	started func(),
) (RunResult, error) {
	machtianiID := session.SessionID
	if !isCanonicalConversationReference(machtianiID) {
		return RunResult{}, fmt.Errorf("agent session ID must be a canonical conversation reference")
	}
	modelArgs, err := machtianiconfig.RunModelArgs(r.model)
	if err != nil {
		return RunResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return RunResult{}, fmt.Errorf("create agent output directory: %w", err)
	}
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return RunResult{}, fmt.Errorf("clear prior agent final answer: %w", err)
	}
	args := []string{"run"}
	if prompt != nil {
		args = append(args, "--prompt", *prompt)
	}
	if !session.IsNew {
		args = append(args, "--resume", machtianiID)
	}
	args = append(args, modelArgs...)
	if len(r.backends) == 0 || r.manager == "" {
		return RunResult{}, fmt.Errorf("agent-managed mode is not configured")
	}
	encodedBackends, err := backendcatalog.EncodeWithCustom(r.backends, r.customBackends)
	if err != nil {
		return RunResult{}, fmt.Errorf("encode configured agent backends: %w", err)
	}
	if r.magnificaHumanitas {
		args = append(args, "--magnifica-humanitas")
	}
	args = append(
		args,
		"--mode", "agent-managed",
		"--no-banner",
		"--no-cursor",
		"--final-file", finalPath,
	)

	command := exec.CommandContext(ctx, r.binary, args...)
	command.Cancel = func() error { return r.cancel(command) }
	command.WaitDelay = 5 * time.Second
	command.Dir = r.projectDir
	command.Env = unsetEnv(os.Environ(), "DEARMACHINE_ATTACHMENTS_INBOX")
	command.Env = unsetEnv(command.Env, "DEARMACHINE_ATTACHMENTS_OUTBOX")
	command.Env = unsetEnv(command.Env, "DEARMACHINE_ATTACHMENTS_MANIFEST")
	command.Env = unsetEnv(command.Env, "MACHTIANI_SESSION_ID")
	command.Env = unsetEnv(command.Env, "DEARMACHINE_BACKEND")
	command.Env = unsetEnv(command.Env, backendcatalog.EnvironmentVariable)
	command.Env = unsetEnv(command.Env, "AGENT_MANAGER_PATH")
	command.Env = setEnv(command.Env, backendcatalog.EnvironmentVariable, encodedBackends)
	command.Env = setEnv(command.Env, "AGENT_MANAGER_PATH", r.manager)
	command.Env = setEnv(command.Env, "DEARMACHINE_ATTACHMENTS_INBOX", tc.InboxPath)
	command.Env = setEnv(command.Env, "DEARMACHINE_ATTACHMENTS_OUTBOX", tc.OutboxPath)
	command.Env = setEnv(command.Env, "DEARMACHINE_ATTACHMENTS_MANIFEST", tc.ManifestPath)
	if session.IsNew {
		command.Env = setEnv(command.Env, "MACHTIANI_SESSION_ID", machtianiID)
	}
	var runOutput bytes.Buffer
	command.Stdout = &runOutput
	command.Stderr = &runOutput
	err = r.runActiveCommand(ctx, session.ThreadID, command, started)
	stopped := r.releaseActive(session.ThreadID, command)
	if stopped {
		return RunResult{}, fmt.Errorf("%w: thread %s", ErrGracefullyStopped, session.ThreadID)
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return RunResult{}, fmt.Errorf("machtiani run failed: %w: %s", err, strings.TrimSpace(runOutput.String()))
	}

	state, err := r.showSession(ctx, machtianiID)
	if err != nil {
		return RunResult{}, err
	}
	result, ready, err := resultFromState(state, finalPath)
	if err != nil {
		return RunResult{}, err
	}
	if !ready {
		if state.Status == "success" {
			return RunResult{}, fmt.Errorf("machtiani reported success without a final answer file")
		}
		return RunResult{}, fmt.Errorf("machtiani returned unsupported status %q", state.Status)
	}
	return result, nil
}

func (r *AgentRunner) runActiveCommand(
	ctx context.Context,
	threadID string,
	command *exec.Cmd,
	started func(),
) error {
	if err := machtianiconfig.Apply(command); err != nil {
		return err
	}
	guard, _ := ctx.Value(guestStartContextKey{}).(guestStartGuard)
	if guard == nil {
		guard = func(start func() error) error { return start() }
	}
	if r.invoke != nil {
		// Injected invokers model the entire command lifecycle in component tests.
		return guard(func() error {
			r.registerActive(threadID, command)
			if started != nil {
				started()
			}
			return r.invoke(command)
		})
	}
	if err := guard(command.Start); err != nil {
		return err
	}
	// Do not expose Cmd to Stop until Start has finished initializing Process.
	// This is also the launch boundary used by queue-aware grace preemption.
	r.registerActive(threadID, command)
	if started != nil {
		started()
	}
	return command.Wait()
}

func (r *AgentRunner) Recover(
	ctx context.Context,
	session Session,
	originalPrompt,
	finalPath string,
	tc TurnContext,
) (RunResult, error) {
	return r.recoverObserved(ctx, session, originalPrompt, finalPath, tc, nil)
}

func (r *AgentRunner) recoverObserved(
	ctx context.Context,
	session Session,
	originalPrompt,
	finalPath string,
	tc TurnContext,
	started func(),
) (RunResult, error) {
	if err := r.ValidateTurnContext(tc); err != nil {
		return RunResult{}, fmt.Errorf("validate turn context: %w", err)
	}
	machtianiID := session.SessionID
	if !isCanonicalConversationReference(machtianiID) {
		return RunResult{}, fmt.Errorf("agent session ID must be a canonical conversation reference")
	}
	state, err := r.showSession(ctx, machtianiID)
	if err != nil || state.Goal != originalPrompt {
		return r.runObserved(ctx, session, originalPrompt, finalPath, tc, started)
	}
	result, ready, err := resultFromState(state, finalPath)
	if err != nil {
		return RunResult{}, err
	}
	if ready {
		return result, nil
	}

	resumed := session
	resumed.IsNew = false
	return r.runResumeObserved(ctx, resumed, finalPath, tc, started)
}

func (r *AgentRunner) DeleteSession(ctx context.Context, sessionID string) error {
	if !isValidOpaqueSessionID(sessionID) {
		return fmt.Errorf("agent session ID is invalid")
	}
	command := exec.CommandContext(
		ctx,
		r.binary,
		"session", "delete", sessionID,
	)
	command.Dir = r.projectDir
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := r.runCommand(command); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf(
			"machtiani session delete failed: %w: %s",
			err,
			strings.TrimSpace(output.String()),
		)
	}
	return nil
}

// ForkSession creates a clean continuation from the committed state of an
// inactive agent session. machtiani excludes disposable shell-agent state from
// the fork, which makes this suitable for abandoning an interrupted turn.
func (r *AgentRunner) ForkSession(
	ctx context.Context,
	sourceID, destinationID string,
) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	destinationID = strings.TrimSpace(destinationID)
	if destinationID == "" {
		if !isCanonicalConversationReference(sourceID) {
			return "", fmt.Errorf("agent session ID must be a canonical conversation reference")
		}
	} else {
		if !isValidOpaqueSessionID(sourceID) {
			return "", fmt.Errorf("agent source session ID is invalid")
		}
		if !isCanonicalConversationReference(destinationID) {
			return "", fmt.Errorf("agent destination session ID must be a canonical conversation reference")
		}
	}
	arguments := []string{"session", "fork", sourceID}
	if destinationID != "" {
		arguments = append(arguments, destinationID)
	}
	command := exec.CommandContext(ctx, r.binary, arguments...)
	command.Dir = r.projectDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := r.runCommand(command); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf(
			"machtiani session fork failed: %w: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	forkedID := strings.TrimSpace(stdout.String())
	if !isValidOpaqueSessionID(forkedID) {
		return "", fmt.Errorf("machtiani session fork returned an invalid session ID")
	}
	if forkedID == sourceID {
		return "", fmt.Errorf("machtiani session fork returned the source session ID")
	}
	if destinationID != "" && forkedID != destinationID {
		return "", fmt.Errorf(
			"machtiani session fork returned %q instead of destination %q",
			forkedID,
			destinationID,
		)
	}
	return forkedID, nil
}

// EnsureForkSession makes the explicit-destination fork safe to retry after a
// process stops between the filesystem fork and the SQLite state transition.
func (r *AgentRunner) EnsureForkSession(ctx context.Context, sourceID, destinationID string) error {
	if _, err := r.ForkSession(ctx, sourceID, destinationID); err != nil {
		if _, showErr := r.showSession(ctx, destinationID); showErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func isValidOpaqueSessionID(sessionID string) bool {
	return sessionID != "" &&
		sessionID == strings.TrimSpace(sessionID) &&
		len(strings.Fields(sessionID)) == 1 &&
		!strings.ContainsAny(sessionID, "\r\n\x00")
}

func RemoveRecoveryResult(sessionID, messageID string) error {
	path := recoveryResultPath(sessionID, messageID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove abandoned agent result: %w", err)
	}
	return nil
}

func resultFromState(
	state sessionState,
	finalPath string,
) (RunResult, bool, error) {
	switch state.Status {
	case "success":
		answer, err := os.ReadFile(finalPath)
		if os.IsNotExist(err) {
			return RunResult{}, false, nil
		}
		if err != nil {
			return RunResult{}, false, fmt.Errorf("read agent final answer: %w", err)
		}
		text := strings.TrimSpace(string(answer))
		if text == "" {
			return RunResult{}, false, fmt.Errorf(
				"machtiani reported success with an empty final answer",
			)
		}
		var magnificaHumanitas *MagnificaHumanitas
		if state.MagnificaHumanitas != nil {
			value := *state.MagnificaHumanitas
			magnificaHumanitas = &value
		}
		return RunResult{
			Kind:               ResultAnswer,
			Text:               text,
			MagnificaHumanitas: magnificaHumanitas,
		}, true, nil
	case "suspended_user_input":
		if state.SuspendedUserInput == nil ||
			strings.TrimSpace(state.SuspendedUserInput.Question) == "" {
			return RunResult{}, false, fmt.Errorf("machtiani suspended without a question")
		}
		return RunResult{
			Kind: ResultQuestion,
			Text: clarificationText(
				state.SuspendedUserInput.Question,
				state.SuspendedUserInput.Context,
			),
		}, true, nil
	default:
		return RunResult{}, false, nil
	}
}

func (r *AgentRunner) showSession(ctx context.Context, sessionID string) (sessionState, error) {
	command := exec.CommandContext(
		ctx,
		r.binary,
		"session", "show", sessionID, "--json",
	)
	command.Dir = r.projectDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := r.runCommand(command); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return sessionState{}, fmt.Errorf(
			"machtiani session show failed: %w: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}

	var state sessionState
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		return sessionState{}, fmt.Errorf("parse machtiani session status: %w", err)
	}
	return state, nil
}

func (r *AgentRunner) runCommand(command *exec.Cmd) error {
	if err := machtianiconfig.Apply(command); err != nil {
		return err
	}
	if r.invoke != nil {
		return r.invoke(command)
	}
	return command.Run()
}

func clarificationText(question, context string) string {
	var body strings.Builder
	body.WriteString("Your computer has a question about your request:\n\n")
	body.WriteString(strings.TrimSpace(question))
	if strings.TrimSpace(context) != "" {
		body.WriteString("\n\n")
		body.WriteString(strings.TrimSpace(context))
	}
	body.WriteString("\n\nReply to this email to answer.")
	return body.String()
}

func setEnv(environment []string, key, value string) []string {
	return append(unsetEnv(environment, key), key+"="+value)
}

func unsetEnv(environment []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return result
}
