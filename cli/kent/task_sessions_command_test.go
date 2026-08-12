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
	return serverapi.WorkflowTaskGetResponse{
		Task: serverapi.WorkflowTaskDetail{Summary: serverapi.WorkflowTaskSummary{ID: taskID}},
	}, nil
}

func (s *taskSessionsCommandStub) ListWorkflowTaskSessions(_ context.Context, request serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error) {
	s.listRequests = append(s.listRequests, request)
	return s.response, nil
}

func TestWriteTaskSessionsResponse(t *testing.T) {
	sessionName := "Custom session-1 label"
	nodeName := "Review"
	exactID := "session-4"
	nextOffset := 4
	response := taskSessionsResponse([]serverapi.WorkflowTaskSessionItem{
		{SessionID: "session-1", SessionName: &sessionName, AgentRole: "coder", Status: serverapi.WorkflowTaskSessionStatusRunning},
		{SessionID: "session-2", NodeName: &nodeName, AgentRole: "reviewer", Status: serverapi.WorkflowTaskSessionStatusQuestion},
		{SessionID: "session-3", AgentRole: "coder", Status: serverapi.WorkflowTaskSessionStatusIdle},
		{SessionID: exactID, SessionName: &exactID, AgentRole: "coder", Status: serverapi.WorkflowTaskSessionStatusIdle},
	}, &nextOffset)
	var stdout, stderr bytes.Buffer
	if code := writeTaskSessionsResponse(&stdout, &stderr, response, false); code != 0 {
		t.Fatalf("human exit=%d stderr=%q", code, stderr.String())
	}
	lines := bytes.Split(bytes.TrimSuffix(stdout.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(lines) != len(response.Items) {
		t.Fatalf("rows=%d, want one per item=%d", len(lines), len(response.Items))
	}
	for index, required := range [][]byte{
		[]byte(sessionName),
		[]byte(nodeName),
		[]byte("coder"),
		[]byte(exactID),
	} {
		if !bytes.Contains(lines[index], required) {
			t.Fatalf("row %d = %q, want selected label %q", index, lines[index], required)
		}
	}
	for _, status := range []serverapi.WorkflowTaskSessionStatus{
		serverapi.WorkflowTaskSessionStatusRunning,
		serverapi.WorkflowTaskSessionStatusQuestion,
		serverapi.WorkflowTaskSessionStatusIdle,
	} {
		label, err := taskSessionStatusText(status)
		if err != nil || label == "" {
			t.Fatalf("status %q mapped to %q, error=%v", status, label, err)
		}
	}
	if bytes.Count(lines[0], []byte("session-1")) != 2 ||
		bytes.Count(lines[3], []byte(exactID)) != 1 ||
		stderr.String() != nextOffsetLine(nextOffset)+"\n" {
		t.Fatalf("human output structure invalid: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := writeTaskSessionsResponse(&stdout, &stderr, taskSessionsResponse([]serverapi.WorkflowTaskSessionItem{}, nil), false); code != 0 ||
		stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("empty human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	controlName := "Review\nLine\r\t\x1b\u2028"
	controlResponse := taskSessionsResponse([]serverapi.WorkflowTaskSessionItem{{
		SessionID: "session-1", SessionName: &controlName, AgentRole: "coder",
		Status: serverapi.WorkflowTaskSessionStatusRunning,
	}}, nil)
	if code := writeTaskSessionsResponse(&stdout, &stderr, controlResponse, false); code != 0 ||
		bytes.Count(stdout.Bytes(), []byte{'\n'}) != 1 ||
		bytes.ContainsAny(stdout.Bytes(), "\r\t\x1b") ||
		bytes.Contains(stdout.Bytes(), []byte("\u2028")) {
		t.Fatalf("escaped human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, escaped := range []string{`\n`, `\r`, `\t`, `\x1b`, `\u2028`} {
		if !bytes.Contains(stdout.Bytes(), []byte(escaped)) {
			t.Fatalf("human output %q lacks %q", stdout.String(), escaped)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := writeTaskSessionsResponse(&stdout, &stderr, controlResponse, true); code != 0 {
		t.Fatalf("JSON exit=%d stderr=%q", code, stderr.String())
	}
	var decoded serverapi.WorkflowTaskSessionListResponse
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil ||
		decoded.Items[0].SessionName == nil || *decoded.Items[0].SessionName != controlName {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if code := writeTaskSessionsResponse(failingCLIWriter{}, &stderr, controlResponse, false); code != 1 || stderr.Len() == 0 {
		t.Fatalf("failed write exit=%d stderr=%q", code, stderr.String())
	}
}

func TestRunTaskSessionsResolvesSelectors(t *testing.T) {
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
			stub := &taskSessionsCommandStub{response: taskSessionsResponse([]serverapi.WorkflowTaskSessionItem{}, nil)}
			stub.response.TaskID = test.taskID
			var stdout, stderr bytes.Buffer
			if code := runTaskSessions(
				t.Context(), config.App{}, stub, stub, test.projectRef, test.selector, 3, 7, false, &stdout, &stderr,
			); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			request := stub.listRequests[0]
			if request.TaskID != test.taskID || request.Offset == nil || *request.Offset != 3 ||
				request.Limit == nil || *request.Limit != 7 {
				t.Fatalf("get=%+v list=%+v", stub.getRequests, stub.listRequests)
			}
			get := stub.getRequests[0]
			if test.name == "short ID" && (get.ProjectID != "project-1" || get.ShortID != "KENT-1") {
				t.Fatalf("short-ID request=%+v", get)
			}
			if test.name == "internal ID" && get.TaskID != test.taskID {
				t.Fatalf("internal-ID request=%+v", get)
			}
		})
	}
}

func TestTaskSessionsCommandValidationAndHelp(t *testing.T) {
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
		!bytes.Contains(stderr.Bytes(), []byte("kent task sessions <task>")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--project")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--offset")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--limit")) ||
		!bytes.Contains(stderr.Bytes(), []byte("--json")) {
		t.Fatalf("route help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := taskSubcommand([]string{"--help"}, &stdout, &stderr); code != 0 ||
		!bytes.Contains(stderr.Bytes(), []byte("kent task sessions <short-id-or-task-id>")) {
		t.Fatalf("task help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func taskSessionsResponse(
	items []serverapi.WorkflowTaskSessionItem,
	nextOffset *int,
) serverapi.WorkflowTaskSessionListResponse {
	return serverapi.WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{
			Items: items, NextOffset: nextOffset,
		},
	}
}
