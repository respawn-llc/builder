package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

type taskSessionsCommandStub struct {
	apicontract.ProjectViewService
	apicontract.WorkflowService
	getRequests  []serverapi.WorkflowTaskGetRequest
	listRequests []serverapi.WorkflowTaskOffsetPageRequest
	response     serverapi.WorkflowTaskSessionListResponse
}

func (s *taskSessionsCommandStub) GetWorkflowTask(_ context.Context, request serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	s.getRequests = append(s.getRequests, request)
	taskID := request.TaskID
	if taskID == "" {
		taskID = s.response.TaskID
	}
	return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{ID: taskID},
	}}, nil
}

func (s *taskSessionsCommandStub) ListWorkflowTaskSessions(_ context.Context, request serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error) {
	s.listRequests = append(s.listRequests, request)
	return s.response, nil
}

func TestWriteTaskSessionsHumanFallbacksAndContinuation(t *testing.T) {
	sessionName := "Custom session-1 label"
	nodeName := "Review"
	exactID := "session-4"
	nextOffset := 4
	response := serverapi.WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{
			Items: []serverapi.WorkflowTaskSessionItem{
				{SessionID: "session-1", SessionName: &sessionName, AgentRole: "coder", Status: serverapi.WorkflowTaskSessionStatusRunning},
				{SessionID: "session-2", NodeName: &nodeName, AgentRole: "reviewer", Status: serverapi.WorkflowTaskSessionStatusQuestion},
				{SessionID: "session-3", AgentRole: "coder", Status: serverapi.WorkflowTaskSessionStatusIdle},
				{SessionID: exactID, SessionName: &exactID, AgentRole: "coder", Status: serverapi.WorkflowTaskSessionStatusIdle},
			},
			NextOffset: &nextOffset,
		},
	}
	var stdout, stderr bytes.Buffer
	if code := writeTaskSessionsResponse(&stdout, &stderr, response, false); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	want := "Custom session-1 label (session-1): Running\n" +
		"Review (session-2): Question\n" +
		"coder (session-3): Idle\n" +
		"session-4: Idle\n"
	if stdout.String() != want || stderr.String() != nextOffsetLine(nextOffset)+"\n" {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestWriteTaskSessionsEmptyAndJSON(t *testing.T) {
	response := serverapi.WorkflowTaskSessionListResponse{
		TaskID:             "task-1",
		WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{Items: []serverapi.WorkflowTaskSessionItem{}},
	}
	var stdout, stderr bytes.Buffer
	if code := writeTaskSessionsResponse(&stdout, &stderr, response, false); code != 0 ||
		stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if code := writeTaskSessionsResponse(&stdout, &stderr, response, true); code != 0 || stderr.Len() != 0 {
		t.Fatalf("JSON exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var decoded serverapi.WorkflowTaskSessionListResponse
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil ||
		decoded.TaskID != response.TaskID || decoded.Items == nil || len(decoded.Items) != 0 {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
}

func TestWriteTaskSessionsHumanPropagatesStdoutFailure(t *testing.T) {
	sessionID := "session-1"
	for name, sessionName := range map[string]*string{
		"labeled row":       nil,
		"exact-ID fallback": &sessionID,
	} {
		t.Run(name, func(t *testing.T) {
			response := serverapi.WorkflowTaskSessionListResponse{
				TaskID: "task-1",
				WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{
					Items: []serverapi.WorkflowTaskSessionItem{{
						SessionID:   sessionID,
						SessionName: sessionName,
						AgentRole:   "coder",
						Status:      serverapi.WorkflowTaskSessionStatusRunning,
					}},
				},
			}
			var stderr bytes.Buffer
			if code := writeTaskSessionsResponse(failingCLIWriter{}, &stderr, response, false); code != 1 ||
				stderr.Len() == 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestWriteTaskSessionsHumanEscapesControlCharactersWithoutChangingJSON(t *testing.T) {
	sessionName := "Review\nLine\r\t\x1b\u2028"
	response := serverapi.WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{
			Items: []serverapi.WorkflowTaskSessionItem{{
				SessionID:   "session-1",
				SessionName: &sessionName,
				AgentRole:   "coder",
				Status:      serverapi.WorkflowTaskSessionStatusRunning,
			}},
		},
	}

	var humanStdout, humanStderr bytes.Buffer
	if code := writeTaskSessionsResponse(&humanStdout, &humanStderr, response, false); code != 0 {
		t.Fatalf("human exit=%d stderr=%q", code, humanStderr.String())
	}
	if bytes.Count(humanStdout.Bytes(), []byte{'\n'}) != 1 ||
		bytes.ContainsAny(humanStdout.Bytes(), "\r\t\x1b") ||
		bytes.Contains(humanStdout.Bytes(), []byte("\u2028")) {
		t.Fatalf("human output contains a line-breaking or control character: %q", humanStdout.String())
	}
	for _, escaped := range []string{`\n`, `\r`, `\t`, `\x1b`, `\u2028`} {
		if !bytes.Contains(humanStdout.Bytes(), []byte(escaped)) {
			t.Fatalf("human output %q does not contain escaped %q", humanStdout.String(), escaped)
		}
	}

	var jsonStdout, jsonStderr bytes.Buffer
	if code := writeTaskSessionsResponse(&jsonStdout, &jsonStderr, response, true); code != 0 {
		t.Fatalf("JSON exit=%d stderr=%q", code, jsonStderr.String())
	}
	var decoded serverapi.WorkflowTaskSessionListResponse
	if err := json.Unmarshal(jsonStdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if decoded.Items[0].SessionName == nil || *decoded.Items[0].SessionName != sessionName {
		t.Fatalf("JSON Session name = %q", *decoded.Items[0].SessionName)
	}
}

func TestRunTaskSessionsResolvesShortAndInternalTaskIDs(t *testing.T) {
	for _, test := range []struct {
		name       string
		selector   string
		projectRef string
		taskID     string
	}{
		{name: "short ID", selector: "KENT-1", projectRef: "project-1", taskID: "task-resolved"},
		{name: "internal ID", selector: "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", projectRef: "ignored", taskID: "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &taskSessionsCommandStub{response: serverapi.WorkflowTaskSessionListResponse{
				TaskID:             test.taskID,
				WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{Items: []serverapi.WorkflowTaskSessionItem{}},
			}}
			var stdout, stderr bytes.Buffer
			if code := runTaskSessions(
				t.Context(),
				config.App{},
				stub,
				stub,
				test.projectRef,
				test.selector,
				3,
				7,
				false,
				&stdout,
				&stderr,
			); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			if len(stub.listRequests) != 1 ||
				stub.listRequests[0].TaskID != test.taskID ||
				stub.listRequests[0].Offset == nil || *stub.listRequests[0].Offset != 3 ||
				stub.listRequests[0].Limit == nil || *stub.listRequests[0].Limit != 7 {
				t.Fatalf("get=%+v list=%+v", stub.getRequests, stub.listRequests)
			}
			if test.name == "short ID" &&
				(len(stub.getRequests) != 1 || stub.getRequests[0].ProjectID != "project-1" || stub.getRequests[0].ShortID != "KENT-1") {
				t.Fatalf("short-ID get request = %+v", stub.getRequests)
			}
			if test.name == "internal ID" &&
				(len(stub.getRequests) != 1 || stub.getRequests[0].TaskID != test.taskID) {
				t.Fatalf("internal-ID get request = %+v", stub.getRequests)
			}
		})
	}
}

func TestTaskSessionsCommandRejectsUsageAndPaginationBeforeRemote(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"KENT-1", "extra"},
		{"KENT-1", "--offset", "-1"},
		{"KENT-1", "--limit", "0"},
		{"KENT-1", "--limit", "101"},
	} {
		var stdout, stderr bytes.Buffer
		if code := taskSessionsSubcommand(args, &stdout, &stderr); code != 2 ||
			stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := taskSubcommand([]string{"sessions", "--help"}, &stdout, &stderr); code != 0 ||
		stdout.Len() != 0 ||
		!bytes.Contains(stderr.Bytes(), []byte("kent task sessions <task>")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--project")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--offset")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--limit")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--json")) {
		t.Fatalf("route help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := taskSubcommand([]string{"--help"}, &stdout, &stderr); code != 0 ||
		!bytes.Contains(stderr.Bytes(), []byte("kent task sessions <short-id-or-task-id>")) {
		t.Fatalf("task help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
