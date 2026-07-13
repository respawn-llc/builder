package main

import (
	"testing"

	"core/shared/serverapi"
)

func TestParseTaskExecutionTargetSelector(t *testing.T) {
	for _, test := range []struct {
		raw       string
		mode      serverapi.WorkflowExecutionTargetMode
		customRef string
	}{
		{raw: "none", mode: serverapi.WorkflowExecutionTargetModeNone},
		{raw: "head", mode: serverapi.WorkflowExecutionTargetModeHead},
		{raw: "default-branch", mode: serverapi.WorkflowExecutionTargetModeDefaultBranch},
		{raw: "ref:refs/tags/v1", mode: serverapi.WorkflowExecutionTargetModeCustomRef, customRef: "refs/tags/v1"},
	} {
		selection, err := parseTaskExecutionTargetSelector(test.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", test.raw, err)
		}
		if selection.Mode != test.mode {
			t.Fatalf("parse %q mode = %q, want %q", test.raw, selection.Mode, test.mode)
		}
		if test.customRef == "" {
			if selection.CustomRef != nil {
				t.Fatalf("parse %q custom ref = %v, want absent", test.raw, selection.CustomRef)
			}
		} else if selection.CustomRef == nil || *selection.CustomRef != test.customRef {
			t.Fatalf("parse %q custom ref = %v, want %q", test.raw, selection.CustomRef, test.customRef)
		}
	}

	for _, raw := range []string{"", "default_branch", "ask-on-first-execution", "ref:", "ref:   ", "future"} {
		if _, err := parseTaskExecutionTargetSelector(raw); err == nil {
			t.Fatalf("parseTaskExecutionTargetSelector(%q) succeeded", raw)
		}
	}
}

func TestParseWorkflowExecutionTargetPolicySelector(t *testing.T) {
	policy, err := parseWorkflowExecutionTargetPolicySelector("ask-on-first-execution")
	if err != nil {
		t.Fatalf("parse ask-on-first-execution: %v", err)
	}
	if policy.Mode != serverapi.WorkflowExecutionTargetModeAskOnFirstExecution || policy.CustomRef != nil {
		t.Fatalf("ask policy = %+v", policy)
	}

	policy, err = parseWorkflowExecutionTargetPolicySelector("ref:release/v1")
	if err != nil {
		t.Fatalf("parse custom ref: %v", err)
	}
	if policy.Mode != serverapi.WorkflowExecutionTargetModeCustomRef || policy.CustomRef == nil || *policy.CustomRef != "release/v1" {
		t.Fatalf("custom policy = %+v", policy)
	}
}
