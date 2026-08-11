package main

import (
	"bytes"
	"context"
	"core/shared/client"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"core/prompts"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

type taskCompleteArgs struct {
	SessionID      string
	TaskRef        string
	ProjectRef     string
	TransitionID   string
	Commentary     string
	Force          bool
	JSONPayload    string
	JSONFile       string
	JSONPayloadSet bool
	JSONFileSet    bool
	OutputValues   map[string]string
	FieldFlagsUsed bool
}

func taskCompleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	parsed, ok, exitCode := parseTaskCompleteArgs(args, stderr)
	if !ok {
		return exitCode
	}
	agentSessionID, agentContext := sessionenv.LookupSessionID(os.LookupEnv)
	if agentContext && parsed.Force {
		fmt.Fprintln(stderr, prompts.WorkflowHumanOnlyTaskActionDeniedPrompt)
		return 1
	}
	if !agentContext && !parsed.Force {
		fmt.Fprintln(stderr, prompts.WorkflowTaskCompleteHumanSafetyWarningPrompt)
		return 1
	}
	if count := parsed.selectorCount(); count > 1 {
		fmt.Fprintln(stderr, "at most one completion target selector is allowed")
		return 2
	} else if !agentContext && count != 1 {
		fmt.Fprintln(stderr, "task complete --force requires exactly one explicit selector: --session or --task")
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		req, err := parsed.request(context.Background(), cfg, remote, remote, agentSessionID, agentContext)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := req.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		resp, err := remote.CompleteWorkflowTask(ctx, req)
		if err != nil {
			fmt.Fprintln(stderr, taskCompleteErrorMessage(err))
			return 1
		}
		if parsed.JSONPayloadSet || parsed.JSONFileSet {
			return writeCommandJSON(stdout, stderr, taskCompleteJSONResponse{
				TaskID:            resp.TaskID,
				CurrentNodes:      resp.CurrentNodes,
				PendingApprovalID: resp.PendingApprovalID,
			})
		}
		writeTaskCompleteResult(stdout, resp)
		return 0
	})
}

func (a taskCompleteArgs) selectorCount() int {
	count := 0
	for _, value := range []string{a.SessionID, a.TaskRef} {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func (a taskCompleteArgs) request(
	ctx context.Context,
	cfg config.App,
	projects apicontract.ProjectViewService,
	workflows apicontract.WorkflowService,
	agentSessionID string,
	agentContext bool,
) (serverapi.WorkflowTaskCompleteRequest, error) {
	req := serverapi.WorkflowTaskCompleteRequest{
		SessionID:    strings.TrimSpace(a.SessionID),
		TransitionID: strings.TrimSpace(a.TransitionID),
		OutputValues: maps.Clone(a.OutputValues),
		Commentary:   a.Commentary,
	}
	if len(req.OutputValues) == 0 {
		req.OutputValues = nil
	}
	if agentContext {
		req.ActorKind = serverapi.WorkflowTaskCompleteActorAgent
		req.AgentSessionID = strings.TrimSpace(agentSessionID)
	} else {
		req.ActorKind = serverapi.WorkflowTaskCompleteActorUser
		req.Force = a.Force
	}
	taskRef := strings.TrimSpace(a.TaskRef)
	if taskRef == "" {
		return req, nil
	}
	taskID, err := resolveWorkflowTaskID(ctx, cfg, projects, workflows, a.ProjectRef, taskRef)
	if err != nil {
		return serverapi.WorkflowTaskCompleteRequest{}, err
	}
	req.TaskID = taskID
	return req, nil
}

func parseTaskCompleteArgs(args []string, stderr io.Writer) (taskCompleteArgs, bool, int) {
	parsed := taskCompleteArgs{ProjectRef: ".", OutputValues: map[string]string{}}
	for index := 0; index < len(args); index++ {
		raw := args[index]
		name, inlineValue, hasInlineValue, ok := taskCompleteFlag(raw)
		if !ok {
			fmt.Fprintf(stderr, "task complete does not accept positional arguments: %s\n", raw)
			return taskCompleteArgs{}, false, 2
		}
		switch name {
		case "help", "h":
			writeTaskCompleteUsage(stderr)
			return taskCompleteArgs{}, false, 0
		case "force":
			value, err := taskCompleteBoolFlagValue(inlineValue, hasInlineValue)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			parsed.Force = value
		case "session":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.SessionID = strings.TrimSpace(value)
		case "task":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.TaskRef = strings.TrimSpace(value)
		case "project":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.ProjectRef = strings.TrimSpace(value)
		case "transition":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.TransitionID = value
			parsed.FieldFlagsUsed = true
		case "commentary":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.Commentary = value
			parsed.FieldFlagsUsed = true
		case "param":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			if err := setTaskCompleteOutputValue(parsed.OutputValues, value); err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			parsed.FieldFlagsUsed = true
		case "json":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.JSONPayload = value
			parsed.JSONPayloadSet = true
		case "json-file":
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.JSONFile = strings.TrimSpace(value)
			parsed.JSONFileSet = true
		default:
			value, next, err := taskCompleteStringFlagValue(args, index, inlineValue, hasInlineValue, name)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return taskCompleteArgs{}, false, 2
			}
			index = next
			parsed.OutputValues[name] = value
			parsed.FieldFlagsUsed = true
		}
	}
	if parsed.JSONPayloadSet && parsed.JSONFileSet {
		fmt.Fprintln(stderr, "--json cannot be combined with --json-file")
		return taskCompleteArgs{}, false, 2
	}
	if (parsed.JSONPayloadSet || parsed.JSONFileSet) && parsed.FieldFlagsUsed {
		fmt.Fprintln(stderr, "--json cannot be combined with completion field flags")
		return taskCompleteArgs{}, false, 2
	}
	if parsed.JSONPayloadSet || parsed.JSONFileSet {
		if err := parsed.applyJSONPayload(); err != nil {
			fmt.Fprintln(stderr, err)
			return taskCompleteArgs{}, false, 2
		}
	}
	return parsed, true, 0
}

func taskCompleteFlag(raw string) (string, string, bool, bool) {
	if !strings.HasPrefix(raw, "-") || raw == "-" {
		return "", "", false, false
	}
	trimmed := strings.TrimLeft(raw, "-")
	if trimmed == "" {
		return "", "", false, false
	}
	name, value, hasValue := strings.Cut(trimmed, "=")
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false, false
	}
	return name, value, hasValue, true
}

func taskCompleteStringFlagValue(args []string, index int, inlineValue string, hasInlineValue bool, name string) (string, int, error) {
	if hasInlineValue {
		return inlineValue, index, nil
	}
	next := index + 1
	if next >= len(args) {
		return "", index, fmt.Errorf("--%s requires a value", name)
	}
	if strings.HasPrefix(args[next], "-") && args[next] != "-" {
		return "", index, fmt.Errorf("--%s requires a value", name)
	}
	return args[next], next, nil
}

func taskCompleteBoolFlagValue(inlineValue string, hasInlineValue bool) (bool, error) {
	if !hasInlineValue {
		return true, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(inlineValue))
	if err != nil {
		return false, fmt.Errorf("--force requires a boolean value when assigned with '='")
	}
	return value, nil
}

func setTaskCompleteOutputValue(values map[string]string, raw string) error {
	name, value, ok := strings.Cut(raw, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return fmt.Errorf("param must be name=value")
	}
	values[name] = value
	return nil
}

func (a *taskCompleteArgs) applyJSONPayload() error {
	raw := a.JSONPayload
	if a.JSONFileSet {
		content, err := os.ReadFile(a.JSONFile)
		if err != nil {
			return fmt.Errorf("read --json-file: %w", err)
		}
		raw = string(content)
	}
	fields, err := parseTaskCompleteJSONPayload(raw)
	if err != nil {
		return err
	}
	a.TransitionID = fields.TransitionID
	a.Commentary = fields.Commentary
	a.OutputValues = fields.OutputValues
	return nil
}

type taskCompleteJSONFields struct {
	TransitionID string
	Commentary   string
	OutputValues map[string]string
}

func parseTaskCompleteJSONPayload(raw string) (taskCompleteJSONFields, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		return taskCompleteJSONFields{}, fmt.Errorf("parse --json payload: %w", err)
	}
	if payload == nil {
		return taskCompleteJSONFields{}, errors.New("parse --json payload: expected one JSON object")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return taskCompleteJSONFields{}, errors.New("parse --json payload: expected one JSON object")
	}
	out := taskCompleteJSONFields{OutputValues: map[string]string{}}
	if rawOutputValues, ok := payload["output_values"]; ok {
		values, err := taskCompleteJSONOutputValues(rawOutputValues)
		if err != nil {
			return taskCompleteJSONFields{}, err
		}
		for key, value := range values {
			out.OutputValues[key] = value
		}
	}
	for _, key := range sortedRawJSONKeys(payload) {
		switch key {
		case "output_values":
			continue
		case "transition", "transition_id":
			value, ok, err := taskCompleteJSONStringValue(payload[key], key)
			if err != nil {
				return taskCompleteJSONFields{}, err
			}
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(value)
			if out.TransitionID != "" && trimmed != "" && out.TransitionID != trimmed {
				return taskCompleteJSONFields{}, errors.New("parse --json payload: transition and transition_id cannot disagree")
			}
			out.TransitionID = trimmed
		case "commentary":
			value, ok, err := taskCompleteJSONStringValue(payload[key], key)
			if err != nil {
				return taskCompleteJSONFields{}, err
			}
			if ok {
				out.Commentary = value
			}
		case "session_id", "task_id", "actor_kind", "agent_session_id", "force":
			return taskCompleteJSONFields{}, fmt.Errorf("parse --json payload: %s must be passed as a flag, not in the JSON payload", key)
		default:
			value, ok, err := taskCompleteJSONParameterValue(payload[key], key)
			if err != nil {
				return taskCompleteJSONFields{}, err
			}
			if ok {
				out.OutputValues[key] = value
			}
		}
	}
	return out, nil
}

func taskCompleteJSONOutputValues(raw json.RawMessage) (map[string]string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return map[string]string{}, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse --json payload: output_values must be an object")
	}
	values := map[string]string{}
	for _, key := range sortedRawJSONKeys(payload) {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return nil, errors.New("parse --json payload: output_values field name is required")
		}
		value, ok, err := taskCompleteJSONParameterValue(payload[key], "output_values."+trimmed)
		if err != nil {
			return nil, err
		}
		if ok {
			values[trimmed] = value
		}
	}
	return values, nil
}

func taskCompleteJSONStringValue(raw json.RawMessage, field string) (string, bool, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("parse --json payload: %s must be a string", field)
	}
	return value, true, nil
}

func taskCompleteJSONParameterValue(raw json.RawMessage, field string) (string, bool, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "null", true, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true, nil
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, bytes.TrimSpace(raw)); err != nil {
		return "", false, fmt.Errorf("parse --json payload: %s must be valid JSON", field)
	}
	return compacted.String(), true, nil
}

func sortedRawJSONKeys(payload map[string]json.RawMessage) []string {
	return slices.Sorted(maps.Keys(payload))
}

func taskCompleteErrorMessage(err error) string {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound):
		return "no idle or live workflow Session matched the completion selector. Retry with --session <session-id> or --task <task-id-or-short-id>."
	case errors.Is(err, serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous):
		return "the completion selector matched multiple Current Nodes. Retry with --session <session-id> or a narrower task."
	default:
		return err.Error()
	}
}

func writeTaskCompleteUsage(stderr io.Writer) {
	fs := newCommandFlagSet(config.Command+" task complete", stderr, taskCompleteUsage)
	fs.String("session", "", "Session whose idle workflow node should be completed")
	fs.String("task", "", "Task ID or short ID whose unambiguous idle workflow node should be completed")
	fs.String("project", ".", "project ID or attached workspace path used to resolve a task short ID")
	fs.String("transition", "", "selected transition key; required when the node has multiple transitions")
	fs.String("commentary", "", "note recorded with the transition result")
	fs.String("param", "", "transition value as name=value; repeatable")
	fs.String("json", "", "complete from a JSON transition-result object and write the response as JSON")
	fs.String("json-file", "", "read the JSON transition-result object from this file and write the response as JSON")
	fs.Bool("force", false, "allow completion outside Kent shell commands with exactly one target selector")
	fs.Usage()
}

func writeTaskCompleteResult(stdout io.Writer, resp serverapi.WorkflowTaskCompleteResponse) {
	if resp.PendingApprovalID != nil {
		fmt.Fprintf(stdout, "Completion is awaiting approval %s.\n", *resp.PendingApprovalID)
		return
	}
	fmt.Fprintf(stdout, "Completion scheduled. The transition %s → %s will execute now. Your next agent turn will begin with the next workflow instructions.\n", resp.Handoff.SourceNodeDisplayName, resp.Handoff.DestinationDisplayName)
}

type taskCompleteJSONResponse struct {
	TaskID            string                              `json:"task_id"`
	CurrentNodes      []serverapi.WorkflowTaskCurrentNode `json:"current_nodes"`
	PendingApprovalID *string                             `json:"pending_approval_id,omitempty"`
}
