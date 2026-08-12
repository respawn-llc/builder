package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

const taskSessionsDefaultLimit = 100

func taskSessionsSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task sessions", stderr, taskSessionsUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	offset := fs.Int("offset", 0, "zero-based Session offset")
	limit := fs.Int("limit", taskSessionsDefaultLimit, "maximum number of Sessions to return")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task sessions requires <short-id-or-task-id>")
		return 2
	}
	if err := validateWorkflowPagination(*offset, *limit); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote *client.Remote) int {
		return runTaskSessions(
			context.Background(),
			cfg,
			remote,
			remote,
			*projectRef,
			positionals[0],
			*offset,
			*limit,
			*jsonOut,
			stdout,
			stderr,
		)
	})
}

func runTaskSessions(
	ctx context.Context,
	cfg config.App,
	projects apicontract.ProjectViewService,
	workflows apicontract.WorkflowService,
	projectRef string,
	taskRef string,
	offset int,
	limit int,
	jsonOut bool,
	stdout io.Writer,
	stderr io.Writer,
) int {
	taskID, err := resolveWorkflowTaskID(ctx, cfg, projects, workflows, projectRef, taskRef)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	response, err := workflows.ListWorkflowTaskSessions(rpcCtx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: taskID,
		Offset: &offset,
		Limit:  &limit,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeTaskSessionsResponse(stdout, stderr, response, jsonOut)
}

func writeTaskSessionsResponse(
	stdout io.Writer,
	stderr io.Writer,
	response serverapi.WorkflowTaskSessionListResponse,
	jsonOut bool,
) int {
	if jsonOut {
		return writeCommandJSON(stdout, stderr, response)
	}
	for _, item := range response.Items {
		label := item.AgentRole
		if item.NodeName != nil {
			label = *item.NodeName
		}
		if item.SessionName != nil {
			label = *item.SessionName
		}
		status, err := taskSessionStatusText(item.Status)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		humanLabel := taskSessionHumanField(label)
		humanSessionID := taskSessionHumanField(item.SessionID)
		var writeErr error
		if label == item.SessionID {
			_, writeErr = fmt.Fprintf(stdout, "%s: %s\n", humanSessionID, status)
		} else {
			_, writeErr = fmt.Fprintf(stdout, "%s (%s): %s\n", humanLabel, humanSessionID, status)
		}
		if writeErr != nil {
			fmt.Fprintln(stderr, writeErr)
			return 1
		}
	}
	if response.NextOffset != nil {
		if err := writeNextOffset(stderr, *response.NextOffset); err != nil {
			return 1
		}
	}
	return 0
}

func taskSessionHumanField(value string) string {
	var escaped strings.Builder
	for _, current := range value {
		if !unicode.IsControl(current) && current != '\u2028' && current != '\u2029' {
			escaped.WriteRune(current)
			continue
		}
		writeTaskSessionRuneEscape(&escaped, current)
	}
	return escaped.String()
}

func writeTaskSessionRuneEscape(escaped *strings.Builder, current rune) {
	switch current {
	case '\a':
		escaped.WriteString(`\a`)
	case '\b':
		escaped.WriteString(`\b`)
	case '\f':
		escaped.WriteString(`\f`)
	case '\n':
		escaped.WriteString(`\n`)
	case '\r':
		escaped.WriteString(`\r`)
	case '\t':
		escaped.WriteString(`\t`)
	case '\v':
		escaped.WriteString(`\v`)
	default:
		switch {
		case current <= 0xff:
			escaped.WriteString(`\x`)
			writeTaskSessionHex(escaped, uint32(current), 2)
		case current <= 0xffff:
			escaped.WriteString(`\u`)
			writeTaskSessionHex(escaped, uint32(current), 4)
		default:
			escaped.WriteString(`\U`)
			writeTaskSessionHex(escaped, uint32(current), 8)
		}
	}
}

func writeTaskSessionHex(escaped *strings.Builder, value uint32, width int) {
	const hexDigits = "0123456789abcdef"
	for shift := (width - 1) * 4; shift >= 0; shift -= 4 {
		escaped.WriteByte(hexDigits[(value>>uint(shift))&0xf])
	}
}

func taskSessionStatusText(status serverapi.WorkflowTaskSessionStatus) (string, error) {
	switch status {
	case serverapi.WorkflowTaskSessionStatusRunning:
		return "Running", nil
	case serverapi.WorkflowTaskSessionStatusQuestion:
		return "Question", nil
	case serverapi.WorkflowTaskSessionStatusIdle:
		return "Idle", nil
	default:
		return "", fmt.Errorf("unknown Task Session status %q", status)
	}
}
