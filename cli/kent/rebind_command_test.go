package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

func TestParseRebindCommandSelectsHumanAgentAndExplicitSessions(t *testing.T) {
	unsetEnvironmentForTaskContractTest(t, sessionenv.SessionIDEnv)
	var stderr bytes.Buffer
	if _, ok, code := parseRebindCommandArguments([]string{"/target"}, &stderr); ok || code != 2 {
		t.Fatalf("outside Kent parse=(%t,%d), stderr=%q", ok, code, stderr.String())
	}
	stderr.Reset()
	args, ok, code := parseRebindCommandArguments([]string{"--session", "human-session", "/target"}, &stderr)
	if !ok || code != 0 || args.SessionID != "human-session" {
		t.Fatalf("human parse=(%+v,%t,%d), stderr=%q", args, ok, code, stderr.String())
	}

	t.Setenv(sessionenv.SessionIDEnv, "current-session")
	args, ok, code = parseRebindCommandArguments([]string{"/target"}, &stderr)
	if !ok || code != 0 || args.SessionID != "current-session" {
		t.Fatalf("agent parse=(%+v,%t,%d), stderr=%q", args, ok, code, stderr.String())
	}

	args, ok, code = parseRebindCommandArguments([]string{"--session", "other-session", "/target"}, &stderr)
	if !ok || code != 0 || args.SessionID != "other-session" {
		t.Fatalf("explicit parse=(%+v,%t,%d), stderr=%q", args, ok, code, stderr.String())
	}
}

func TestParseRebindCommandHardCutsOverToOnePositionalPath(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "current-session")
	for _, input := range [][]string{
		nil,
		{"session-1", "/target"},
		{"--session", "session-1"},
		{"--session", " ", "/target"},
		{"--project", " ", "/target"},
	} {
		var stderr bytes.Buffer
		if _, ok, code := parseRebindCommandArguments(input, &stderr); ok || code != 2 || stderr.Len() == 0 {
			t.Fatalf("args=%q parse=(%t,%d), stderr=%q", input, ok, code, stderr.String())
		}
	}
}

func TestNewRebindRequestUsesAgentOriginAndScheduledCompletion(t *testing.T) {
	const (
		runID  = "018fdd67-89ab-4cde-8123-456789abc001"
		stepID = "018fdd67-89ab-4cde-8123-456789abc002"
	)
	t.Setenv(sessionenv.RunIDEnv, runID)
	t.Setenv(sessionenv.StepIDEnv, stepID)
	t.Setenv(sessionenv.SessionIDEnv, "session-1")
	projectID := "project-2"
	request, err := newRebindRequest("session-1", "/target", &projectID)
	if err != nil {
		t.Fatal(err)
	}
	if request.SessionID != "session-1" ||
		request.WorkspaceRoot != "/target" ||
		request.ProjectID == nil || *request.ProjectID != projectID ||
		request.CompletionMode != serverapi.SessionRetargetCompletionScheduled ||
		request.Origin == nil ||
		request.Origin.RunID != runID ||
		request.Origin.StepID != stepID ||
		request.OperationID.String() == "" {
		t.Fatalf("request=%+v", request)
	}
}

func TestNewRebindRequestOmitsAgentOriginForAnotherSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "current-session")
	t.Setenv(sessionenv.RunIDEnv, "018fdd67-89ab-4cde-8123-456789abc001")
	t.Setenv(sessionenv.StepIDEnv, "018fdd67-89ab-4cde-8123-456789abc002")

	request, err := newRebindRequest("other-session", "/target", nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.SessionID != "other-session" || request.Origin != nil {
		t.Fatalf("other-Session request=%+v", request)
	}
}

func TestWriteRebindAcknowledgementSupportsHumanAndJSONOutput(t *testing.T) {
	ack := serverapi.WorktreeScheduledAcknowledgement{OperationID: serverapi.NewWorktreeOperationID()}
	for _, jsonOutput := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		if code := writeRebindAcknowledgement(&stdout, &stderr, ack, jsonOutput); code != 0 || stderr.Len() != 0 {
			t.Fatalf("json=%t exit=%d stderr=%q", jsonOutput, code, stderr.String())
		}
		if jsonOutput {
			var decoded serverapi.WorktreeScheduledAcknowledgement
			if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded != ack {
				t.Fatalf("json=%q decoded=%+v err=%v", stdout.String(), decoded, err)
			}
		} else if strings.TrimSpace(stdout.String()) == "" {
			t.Fatal("human acknowledgement was empty")
		}
	}
}

func TestSessionRetargetGuidanceUsesFlagSessionSyntaxAndWorktreeErrors(t *testing.T) {
	retargetErr := &serverapi.SessionRetargetError{
		Reason:        serverapi.SessionRetargetTargetProjectRequired,
		SessionID:     "session-1",
		SourceProject: serverapi.ProjectReference{ID: "project-1", Name: "One"},
		TargetRoot:    "/target",
		CandidateProjects: []serverapi.ProjectReference{
			{ID: "project-2", Name: "Two"},
		},
	}
	guidance := buildSessionRetargetCommandGuidance("/target", retargetErr)
	if len(guidance.Candidates) != 1 || !slices.Equal(guidance.Candidates[0].Tokens, []string{
		config.Command, "rebind", "--session", "session-1", "--project", "project-2", "/target",
	}) {
		t.Fatalf("candidate guidance=%+v", guidance.Candidates)
	}
	if !slices.Equal(guidance.RebindIntoSource, []string{
		config.Command, "rebind", "--session", "session-1", "/target",
	}) {
		t.Fatalf("source guidance=%q", guidance.RebindIntoSource)
	}

	for _, reason := range []serverapi.SessionRetargetErrorReason{
		serverapi.SessionRetargetSourceWorktree,
		serverapi.SessionRetargetTargetWorktree,
	} {
		err := formatSessionRetargetCommandError("/target", &serverapi.SessionRetargetError{
			Reason:        reason,
			SessionID:     "session-1",
			SourceProject: serverapi.ProjectReference{ID: "project-1", Name: "One"},
			TargetRoot:    "/target",
		})
		if err == nil {
			t.Fatalf("reason=%s error=%v", reason, err)
		}
	}
}
