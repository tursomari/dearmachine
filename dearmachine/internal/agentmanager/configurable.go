package agentmanager

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ConfigurableAdapter is a generic Adapter driven by user-supplied
// configuration.  It allows custom backends to be registered without
// modifying Go source code.
type ConfigurableAdapter struct {
	id           string
	displayName  string
	executable   string   // absolute path or name resolvable via exec.LookPath
	outputFormat string   // "json-stream" or "plain"
	args         []string // extra arguments; $WRITABLE_DIR replaced at launch
	env          []string // extra environment variables (KEY=VALUE)
}

// NewConfigurableAdapter creates a ConfigurableAdapter from the given
// fields.  outputFormat must be "json-stream" or "plain".
func NewConfigurableAdapter(id, executable, outputFormat string, args, env []string) *ConfigurableAdapter {
	return &ConfigurableAdapter{
		id:           id,
		displayName:  id, // display name defaults to ID; may be set later
		executable:   executable,
		outputFormat: outputFormat,
		args:         args,
		env:          env,
	}
}

func (c *ConfigurableAdapter) Name() string       { return c.id }
func (c *ConfigurableAdapter) Executable() string { return c.executable }

func (c *ConfigurableAdapter) Prepare(ctx context.Context, cwd, writableDir string) (Launch, error) {
	args := make([]string, len(c.args))
	for i, arg := range c.args {
		args[i] = strings.ReplaceAll(arg, "$WRITABLE_DIR", writableDir)
	}
	command := exec.CommandContext(ctx, c.executable, args...)
	command.Dir = cwd
	if len(c.env) > 0 {
		command.Env = append(os.Environ(), c.env...)
	}
	return Launch{Command: command}, nil
}

func (c *ConfigurableAdapter) ConsumeStdout(stdout io.Reader, foundSession func(string)) (Observation, error) {
	switch c.outputFormat {
	case "json-stream":
		return consumeJSONStream(stdout, foundSession)
	case "plain":
		return consumePlain(stdout)
	default:
		return Observation{}, fmt.Errorf("unknown output format %q", c.outputFormat)
	}
}

// consumeJSONStream replicates CodexAdapter.ConsumeStdout logic.
func consumeJSONStream(stdout io.Reader, foundSession func(string)) (Observation, error) {
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
		if event.Type == "thread.started" && event.ThreadID != "" && foundSession != nil {
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

// consumePlain replicates ForgeAdapter.ConsumeStdout logic.
func consumePlain(stdout io.Reader) (Observation, error) {
	captured := &tailWriter{limit: 64 * 1024}
	_, err := io.Copy(captured, stdout)
	return Observation{Reply: string(captured.content)}, err
}
