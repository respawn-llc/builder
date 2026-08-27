package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestTaskListJSONPreservesEnrichedResponse(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	otherWorkflowID := runtimeids.NewWorkflowID()
	workflowName := "Delivery"
	nextOffset := 8
	projectWide := serverapi.WorkflowTaskListResponse{
		Scope:                       serverapi.WorkflowTaskListScope{ProjectID: "project-1"},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		NextOffset:                  &nextOffset,
		GeneratedAtUnixMs:           1720000000000,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:          "task-1",
			ShortID:         "KENT-1",
			WorkflowID:      workflowID,
			WorkflowName:    &workflowName,
			Title:           "Project-wide task",
			CreatedAtUnixMs: 1710000000000,
			UpdatedAtUnixMs: 1720000000000,
			Status:          taskContractStatus(serverapi.WorkflowTaskStatusKindActive),
			Labels: []serverapi.WorkflowProjectLabel{
				{ID: "label-2", Name: "shared name"},
				{ID: "label-1", Name: "Alpha"},
			},
			DependencyProgress: &serverapi.WorkflowTaskDependencyProgress{
				SatisfiedCount: 1,
				TotalCount:     2,
			},
		}},
	}
	narrowed := serverapi.WorkflowTaskListResponse{
		Scope: serverapi.WorkflowTaskListScope{
			ProjectID:  "project-1",
			WorkflowID: &otherWorkflowID,
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		GeneratedAtUnixMs:           1720000001000,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:          "task-2",
			ShortID:         "KENT-2",
			WorkflowID:      otherWorkflowID,
			Title:           "Workflow task",
			CreatedAtUnixMs: 1710000001000,
			UpdatedAtUnixMs: 1720000001000,
			ColumnKeys:      func() *[]string { values := []string{}; return &values }(),
			Status:          taskContractStatus(serverapi.WorkflowTaskStatusKindDone),
			Labels:          []serverapi.WorkflowProjectLabel{},
		}},
	}

	for _, test := range []struct {
		name     string
		response serverapi.WorkflowTaskListResponse
	}{
		{
			name:     "project-wide",
			response: projectWide,
		},
		{
			name:     "workflow-narrowed",
			response: narrowed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := writeTaskListResponse(&stdout, &stderr, test.response, true); code != 0 || stderr.Len() != 0 {
				t.Fatalf("JSON exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			var got serverapi.WorkflowTaskListResponse
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode JSON: %v", err)
			}
			if !reflect.DeepEqual(got, test.response) {
				t.Fatalf("response = %+v, want %+v", got, test.response)
			}
		})
	}
}

func TestTaskListHumanProjectWideRendering(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	otherWorkflowID := runtimeids.NewWorkflowID()
	firstWorkflowName := "Delivery"
	secondWorkflowName := "Manual Move Router"
	nextOffset := 8
	response := serverapi.WorkflowTaskListResponse{
		Scope:                       serverapi.WorkflowTaskListScope{ProjectID: "project-1"},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		NextOffset:                  &nextOffset,
		Tasks: []serverapi.WorkflowTaskListItem{
			{
				TaskID:       "task-1",
				ShortID:      "KENT-1",
				WorkflowID:   workflowID,
				WorkflowName: &firstWorkflowName,
				Title:        "First task",
				Status:       taskContractStatus(serverapi.WorkflowTaskStatusKindActive),
				Labels: []serverapi.WorkflowProjectLabel{
					{ID: "label-2", Name: "shared name"},
					{ID: "label-1", Name: "Alpha"},
				},
				DependencyProgress: &serverapi.WorkflowTaskDependencyProgress{
					SatisfiedCount: 1,
					TotalCount:     2,
				},
			},
			{
				TaskID:       "task-2",
				ShortID:      "KENT-2",
				WorkflowID:   otherWorkflowID,
				WorkflowName: &secondWorkflowName,
				Title:        "Second task",
				Status:       taskContractStatus(serverapi.WorkflowTaskStatusKindDone),
				Labels:       []serverapi.WorkflowProjectLabel{},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	if code := writeTaskListResponse(&stdout, &stderr, response, false); code != 0 {
		t.Fatalf("human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got := stderr.String(); got != nextOffsetLine(nextOffset)+"\n" {
		t.Fatalf("pagination continuation = %q, want generated continuation", got)
	}
	lines := assertTaskListCommonHumanLines(t, stdout.Bytes(), 8, []int{0, 5}, response.Tasks)
	labelLine := lines[2]
	remaining, ok := taskListHumanPayloadAfterField(labelLine)
	if !ok {
		t.Fatalf("labels line has an invalid field prefix: %q", labelLine)
	}
	for index, label := range response.Tasks[0].Labels {
		if index > 0 {
			if len(remaining) == 0 || remaining[0] != ' ' {
				t.Fatalf("label %d is missing its separator: %q", index, labelLine)
			}
			remaining = remaining[1:]
		}
		quoted, err := strconv.QuotedPrefix(string(remaining))
		if err != nil {
			t.Fatalf("label %d is not quoted: %q", index, labelLine)
		}
		got, err := strconv.Unquote(quoted)
		if err != nil || got != label.Name {
			t.Fatalf("label %d = %q, want response value %q", index, got, label.Name)
		}
		remaining = remaining[len(quoted):]
	}
	if len(remaining) != 0 {
		t.Fatalf("labels line has trailing data: %q", labelLine)
	}
	for index, lineIndex := range []int{3, 7} {
		workflowName := response.Tasks[index].WorkflowName
		if workflowName == nil {
			t.Fatalf("workflow line has no response workflow name: %q", lines[lineIndex])
		}
		payload, ok := taskListHumanPayloadAfterField(lines[lineIndex])
		if !ok || !bytes.Equal(payload, []byte(*workflowName)) {
			t.Fatalf("workflow line does not preserve the response value: %q", lines[lineIndex])
		}
	}
	wantDependency := []byte(strconv.Itoa(response.Tasks[0].DependencyProgress.SatisfiedCount) + "/" +
		strconv.Itoa(response.Tasks[0].DependencyProgress.TotalCount))
	dependencyFields := bytes.Fields(lines[4])
	if len(dependencyFields) != 2 || dependencyFields[0][len(dependencyFields[0])-1] != ':' ||
		!bytes.Equal(dependencyFields[1], wantDependency) {
		t.Fatalf("dependency line does not preserve the response value: %q", lines[4])
	}

	soleWorkflowName := "Delivery"
	oneWorkflowResponse := serverapi.WorkflowTaskListResponse{
		Scope:                       serverapi.WorkflowTaskListScope{ProjectID: "project-1"},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:       "task-3",
			ShortID:      "KENT-3",
			WorkflowID:   workflowID,
			WorkflowName: &soleWorkflowName,
			Title:        "One workflow task",
			Status:       taskContractStatus(serverapi.WorkflowTaskStatusKindActive),
			Labels:       []serverapi.WorkflowProjectLabel{},
		}},
	}
	stdout.Reset()
	stderr.Reset()
	if code := writeTaskListResponse(&stdout, &stderr, oneWorkflowResponse, false); code != 0 || stderr.Len() != 0 {
		t.Fatalf("one-workflow exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	lines = assertTaskListCommonHumanLines(t, stdout.Bytes(), 2, []int{0}, oneWorkflowResponse.Tasks)
}

func TestTaskListHumanWorkflowNarrowedRendering(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	workflowName := "Delivery"
	emptyColumnKeys := []string{}
	response := serverapi.WorkflowTaskListResponse{
		Scope: serverapi.WorkflowTaskListScope{
			ProjectID:  "project-1",
			WorkflowID: &workflowID,
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []serverapi.WorkflowTaskListItem{
			{
				TaskID:       "task-1",
				ShortID:      "KENT-1",
				WorkflowID:   workflowID,
				WorkflowName: &workflowName,
				Title:        "Narrowed task",
				ColumnKeys:   func() *[]string { values := []string{"build", "deploy"}; return &values }(),
				Status:       taskContractStatus(serverapi.WorkflowTaskStatusKindActive),
			},
			{
				TaskID:       "task-2",
				ShortID:      "KENT-2",
				WorkflowID:   workflowID,
				WorkflowName: &workflowName,
				Title:        "Empty nodes task",
				ColumnKeys:   &emptyColumnKeys,
				Status:       taskContractStatus(serverapi.WorkflowTaskStatusKindDone),
				DependencyProgress: &serverapi.WorkflowTaskDependencyProgress{
					SatisfiedCount: 2,
					TotalCount:     2,
				},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	if code := writeTaskListResponse(&stdout, &stderr, response, false); code != 0 || stderr.Len() != 0 {
		t.Fatalf("human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	lines := assertTaskListCommonHumanLines(t, stdout.Bytes(), 6, []int{0, 3}, response.Tasks)
	currentFields := bytes.Fields(lines[2])
	wantCurrent := bytes.Fields([]byte(strings.Join(*response.Tasks[0].ColumnKeys, ", ")))
	if len(currentFields) != len(wantCurrent)+2 ||
		!bytes.Equal(bytes.Join(currentFields[2:], []byte{' '}), bytes.Join(wantCurrent, []byte{' '})) {
		t.Fatalf("current-nodes line does not preserve response values: %q", lines[2])
	}
	emptyFields := bytes.Fields(lines[5])
	if len(emptyFields) != 3 || len(emptyFields[2]) < 2 ||
		emptyFields[2][0] != '(' || emptyFields[2][len(emptyFields[2])-1] != ')' {
		t.Fatalf("empty current-nodes line has an invalid field shape: %q", lines[5])
	}
}

func assertTaskListCommonHumanLines(
	t *testing.T,
	output []byte,
	expectedLines int,
	headerLineIndexes []int,
	tasks []serverapi.WorkflowTaskListItem,
) [][]byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSuffix(output, []byte{'\n'}), []byte{'\n'})
	if len(lines) != expectedLines {
		t.Fatalf("human output lines=%d, want %d", len(lines), expectedLines)
	}
	if len(headerLineIndexes) != len(tasks) {
		t.Fatalf("header indexes=%d, tasks=%d", len(headerLineIndexes), len(tasks))
	}
	for _, line := range lines {
		if len(line) == 0 {
			t.Fatal("human output contains an empty line")
		}
	}
	for index, lineIndex := range headerLineIndexes {
		task := tasks[index]
		headerFields := bytes.Fields(lines[lineIndex])
		titleFields := bytes.Fields([]byte(task.Title + "."))
		if len(headerFields) == 0 || len(headerFields) != len(titleFields)+1 ||
			!bytes.Equal(headerFields[0], []byte(task.ShortID+":")) ||
			!bytes.Equal(bytes.Join(headerFields[1:], []byte{' '}), bytes.Join(titleFields, []byte{' '})) {
			t.Fatalf("header line does not preserve response values: %q", lines[lineIndex])
		}
		statusFields := bytes.Fields(lines[lineIndex+1])
		if len(statusFields) != 2 || len(statusFields[0]) == 0 ||
			statusFields[0][len(statusFields[0])-1] != ':' ||
			!bytes.Equal(statusFields[1], []byte(task.Status.Kind)) {
			t.Fatalf("status line does not preserve the response value: %q", lines[lineIndex+1])
		}
	}
	return lines
}

func taskListHumanPayloadAfterField(line []byte) ([]byte, bool) {
	fields := bytes.Fields(line)
	if len(fields) < 2 || len(fields[0]) == 0 || fields[0][len(fields[0])-1] != ':' ||
		len(line) <= len(fields[0]) || line[len(fields[0])] != ' ' {
		return nil, false
	}
	return line[len(fields[0])+1:], true
}
