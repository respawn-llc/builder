package workflowstore

import (
	"strings"
	"testing"
)

func TestListCurrentNodesValidatesPriorValueShape(t *testing.T) {
	store, _ := newTestStore(t)

	tests := []struct {
		name        string
		raw         string
		wantError   bool
		wantSummary string
	}{
		{name: "empty", raw: `{"transition_parameters":{}}`},
		{
			name:        "nested strings",
			raw:         `{"transition_parameters":{"review":{"summary":"approved"}}}`,
			wantSummary: "approved",
		},
		{name: "malformed", raw: `{"transition_parameters":`, wantError: true},
		{name: "root null", raw: `null`, wantError: true},
		{name: "root array", raw: `[]`, wantError: true},
		{name: "missing transition parameters", raw: `{}`, wantError: true},
		{name: "extra root field", raw: `{"transition_parameters":{},"extra":true}`, wantError: true},
		{name: "null transition parameters", raw: `{"transition_parameters":null}`, wantError: true},
		{name: "null Transition namespace", raw: `{"transition_parameters":{"review":null}}`, wantError: true},
		{name: "non-string Parameter value", raw: `{"transition_parameters":{"review":{"summary":7}}}`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values, err := priorValuesFromJSON(store.priorValuesContract, test.raw)
			if test.wantError {
				if err == nil {
					t.Fatalf("prior values accepted %s", test.raw)
				}
				if !strings.Contains(err.Error(), "decode current node prior values") {
					t.Fatalf("prior-values error = %v, want owner context", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode prior values: %v", err)
			}
			if test.wantSummary != "" {
				got := values.TransitionParameters["review"]["summary"]
				if got != test.wantSummary {
					t.Fatalf("summary = %q, want %q", got, test.wantSummary)
				}
			}
		})
	}
}

func TestListCurrentNodesUsesStorePriorValueContract(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET prior_node_values_json = '{"transition_parameters":{"review":null}}'
WHERE task_id = ?`, string(task.ID)); err != nil {
		t.Fatalf("persist invalid prior values: %v", err)
	}

	_, err := store.ListCurrentNodes(ctx, task.ID)
	if err == nil {
		t.Fatal("ListCurrentNodes accepted a null Transition parameter namespace")
	}
	if !strings.Contains(err.Error(), "decode current node prior values") {
		t.Fatalf("ListCurrentNodes error = %v, want prior-values context", err)
	}
}
