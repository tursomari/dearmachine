package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dearmachine/dearmachine/internal/client"
)

// up is the only public entry point for creating and changing pairs. It
// intentionally intercepts an empty registry before runStart can take its
// legacy fallback: an operator who explicitly asks to bring the client up is
// guided into pairing, while older direct invocations retain the legacy DB.
func runUp(args []string, getenv func(string) string, deps dependencies) error {
	command, runArgs, err := parseUpArgs(args)
	if err != nil {
		return err
	}
	if command.help {
		return upHelp(deps.stdout)
	}

	registryPath, err := client.DefaultPairRegistryPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil {
		return err
	}
	output := outputOrDiscard(deps.stdout)
	if command.list {
		return printPairs(output, registry.Pairs)
	}

	var selected client.Pair
	switch {
	case command.new:
		selected, err = collectPair(deps.stdin, output)
		if err == nil {
			selected.Active = true
			selected, err = client.CreatePair(deps.userHomeDir, selected)
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Created pair %s (%s).\n", selected.DisplayName, selected.ID)
	case command.switchTo != "":
		selected, err = findRegisteredPair(deps.userHomeDir, command.switchTo)
		if err != nil {
			return err
		}
		if err := setActivePair(deps.userHomeDir, selected.ID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Selected pair %s (%s).\n", selected.DisplayName, selected.ID)
	case len(registry.Pairs) == 0:
		selected, err = collectPair(deps.stdin, output)
		if err == nil {
			selected.Active = true
			selected, err = client.CreatePair(deps.userHomeDir, selected)
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Created pair %s (%s).\n", selected.DisplayName, selected.ID)
	case len(registry.Pairs) == 1:
		selected = registry.Pairs[0]
		if err := setActivePair(deps.userHomeDir, selected.ID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Using pair %s (%s).\n", selected.DisplayName, selected.ID)
	default:
		selected, err = pickPair(deps.stdin, output, registry.Pairs)
		if err != nil {
			return err
		}
		if err := setActivePair(deps.userHomeDir, selected.ID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Using pair %s (%s).\n", selected.DisplayName, selected.ID)
	}
	return runStart(runArgs, getenv, deps, &selected)
}

type upCommand struct {
	new      bool
	list     bool
	help     bool
	switchTo string
}

func parseUpArgs(args []string) (upCommand, []string, error) {
	var command upCommand
	runArgs := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--new":
			command.new = true
		case arg == "--list":
			command.list = true
		case arg == "--help" || arg == "-h":
			command.help = true
		case arg == "--switch":
			index++
			if index == len(args) || strings.TrimSpace(args[index]) == "" {
				return upCommand{}, nil, errors.New("--switch requires a pair ID or display name")
			}
			command.switchTo = args[index]
		case strings.HasPrefix(arg, "--switch="):
			command.switchTo = strings.TrimPrefix(arg, "--switch=")
			if strings.TrimSpace(command.switchTo) == "" {
				return upCommand{}, nil, errors.New("--switch requires a pair ID or display name")
			}
		default:
			runArgs = append(runArgs, arg)
		}
	}
	count := 0
	for _, used := range []bool{command.new, command.list, command.help, command.switchTo != ""} {
		if used {
			count++
		}
	}
	if count > 1 {
		return upCommand{}, nil, errors.New("choose only one of --new, --list, or --switch")
	}
	return command, runArgs, nil
}

func collectPair(input io.Reader, output io.Writer) (client.Pair, error) {
	reader := bufio.NewReader(inputOrEmpty(input))
	fields := []struct {
		label string
		set   func(*client.Pair, string)
	}{
		{"Display name", func(pair *client.Pair, value string) { pair.DisplayName = value }},
		{"Your email address", func(pair *client.Pair, value string) { pair.UserEmail = value }},
		{"Dear Machine address", func(pair *client.Pair, value string) { pair.DearMachineAddress = value }},
		{"Transport name", func(pair *client.Pair, value string) { pair.Transport = value }},
		{"Inbox ID", func(pair *client.Pair, value string) { pair.InboxID = value }},
		{"Allowed addresses (comma-separated)", func(pair *client.Pair, value string) { pair.Allow = strings.Split(value, ",") }},
	}
	if _, err := fmt.Fprintln(output, "Create a paired inbox:"); err != nil {
		return client.Pair{}, err
	}
	var pair client.Pair
	for _, field := range fields {
		if _, err := fmt.Fprintf(output, "%s: ", field.label); err != nil {
			return client.Pair{}, err
		}
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return client.Pair{}, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return client.Pair{}, fmt.Errorf("%s is required", strings.ToLower(field.label))
		}
		field.set(&pair, value)
	}
	return pair, nil
}

func pickPair(input io.Reader, output io.Writer, pairs []client.Pair) (client.Pair, error) {
	defaultIndex := 0
	for index, pair := range pairs {
		if pair.Active {
			defaultIndex = index
		}
	}
	if _, err := fmt.Fprintln(output, "Choose a pair:"); err != nil {
		return client.Pair{}, err
	}
	for index, pair := range pairs {
		marker := ""
		if index == defaultIndex {
			marker = " (default)"
		}
		if _, err := fmt.Fprintf(output, "  %d. %s%s\n", index+1, pair.DisplayName, marker); err != nil {
			return client.Pair{}, err
		}
	}
	if _, err := fmt.Fprintf(output, "Selection [%d]: ", defaultIndex+1); err != nil {
		return client.Pair{}, err
	}
	line, err := bufio.NewReader(inputOrEmpty(input)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return client.Pair{}, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return pairs[defaultIndex], nil
	}
	for index, pair := range pairs {
		if line == fmt.Sprint(index+1) || strings.EqualFold(line, pair.ID) || strings.EqualFold(line, pair.DisplayName) {
			return pair, nil
		}
	}
	return client.Pair{}, fmt.Errorf("unknown pair selection %q", line)
}

func findRegisteredPair(userHomeDir func() (string, error), selection string) (client.Pair, error) {
	state, err := client.ResolvePairState(userHomeDir, selection)
	if err != nil {
		return client.Pair{}, err
	}
	if state.Legacy {
		return client.Pair{}, fmt.Errorf("no pairs are registered")
	}
	return state.Pair, nil
}

func setActivePair(userHomeDir func() (string, error), pairID string) error {
	path, err := client.DefaultPairRegistryPath(userHomeDir)
	if err != nil {
		return err
	}
	registry, err := client.LoadPairRegistry(path)
	if err != nil {
		return err
	}
	found := false
	for index := range registry.Pairs {
		registry.Pairs[index].Active = registry.Pairs[index].ID == pairID
		found = found || registry.Pairs[index].Active
	}
	if !found {
		return fmt.Errorf("pair selection %q is unknown", pairID)
	}
	return client.SavePairRegistry(path, registry)
}

func printPairs(output io.Writer, pairs []client.Pair) error {
	if len(pairs) == 0 {
		_, err := fmt.Fprintln(output, "No pairs are registered.")
		return err
	}
	for _, pair := range pairs {
		active := ""
		if pair.Active {
			active = " (active)"
		}
		if _, err := fmt.Fprintf(output, "%s\t%s%s\n", pair.ID, pair.DisplayName, active); err != nil {
			return err
		}
	}
	return nil
}

func upHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine up [--new | --switch <id-or-name> | --list] [run flags]

Starts a paired inbox. With no registered pairs it opens a guided setup; with
several it offers a numbered selector. A blank selector response, including
non-interactive EOF, uses the active pair (or the first registered pair).
`)
	return err
}

func inputOrEmpty(input io.Reader) io.Reader {
	if input == nil {
		return strings.NewReader("")
	}
	return input
}
