package workflowrunner

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkflowStaticReviewRouterRoutesTypedOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workflow review router is a POSIX shell script")
	}
	tests := []struct {
		name       string
		input      string
		transition string
	}{
		{
			name:       "static review passes to QA",
			input:      `{"code_review_outcome":"ready","compliance_outcome":"approved"}`,
			transition: "qa_ready",
		},
		{
			name:       "bounded implementation correction",
			input:      `{"code_review_outcome":"implementation_fix","compliance_outcome":"approved"}`,
			transition: "static_review_rejected",
		},
		{
			name:       "architecture owns a failed mechanism",
			input:      `{"code_review_outcome":"architecture_rework","compliance_outcome":"implementation_fix"}`,
			transition: "static_review_architecture_rework",
		},
		{
			name:       "design scope decision has highest authority",
			input:      `{"code_review_outcome":"architecture_rework","compliance_outcome":"design_scope_change"}`,
			transition: "static_review_design_scope_change",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := runWorkflowReviewRouter(t, "workflow-static-review-router.sh", test.input)
			if err != nil {
				t.Fatalf("run workflow review router: %v\n%s", err, output)
			}
			var result struct {
				Transition string `json:"transition"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatalf("decode workflow review router output: %v\n%s", err, output)
			}
			if result.Transition != test.transition {
				t.Fatalf("transition = %q, want %q", result.Transition, test.transition)
			}
		})
	}
}

func TestWorkflowQAReviewRouterRoutesTypedOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workflow review router is a POSIX shell script")
	}
	tests := []struct {
		outcome    string
		transition string
	}{
		{outcome: "pass", transition: "review_approved"},
		{outcome: "implementation_fix", transition: "review_rejected"},
		{outcome: "architecture_rework", transition: "review_architecture_rework"},
		{outcome: "design_scope_change", transition: "review_design_scope_change"},
	}
	for _, test := range tests {
		t.Run(test.outcome, func(t *testing.T) {
			output, err := runWorkflowReviewRouter(t, "workflow-qa-review-router.sh", `{"qa_outcome":"`+test.outcome+`"}`)
			if err != nil {
				t.Fatalf("run workflow QA router: %v\n%s", err, output)
			}
			var result struct {
				Transition string `json:"transition"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatalf("decode workflow QA router output: %v\n%s", err, output)
			}
			if result.Transition != test.transition {
				t.Fatalf("transition = %q, want %q", result.Transition, test.transition)
			}
		})
	}
}

func TestWorkflowReviewRoutersRejectInvalidOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workflow review router is a POSIX shell script")
	}
	tests := []struct {
		script string
		input  string
	}{
		{
			script: "workflow-static-review-router.sh",
			input:  `{"code_review_outcome":"blocked","compliance_outcome":"approved"}`,
		},
		{
			script: "workflow-qa-review-router.sh",
			input:  `{"qa_outcome":"blocked"}`,
		},
	}
	for _, test := range tests {
		output, err := runWorkflowReviewRouter(t, test.script, test.input)
		if err == nil {
			t.Fatalf("%s accepted an invalid outcome: %s", test.script, output)
		}
	}
}

func runWorkflowReviewRouter(t *testing.T, script string, input string) ([]byte, error) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	command := exec.Command(filepath.Join(repositoryRoot, "scripts", script))
	command.Stdin = strings.NewReader(input)
	return command.CombinedOutput()
}
