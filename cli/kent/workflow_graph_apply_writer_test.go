package main

import (
	"bytes"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestWorkflowGraphApplyHumanWriterUsesOutcomeSpecificStreams(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		remote     *workflowGraphApplyRemote
		wantExit   int
		wantStdout bool
		wantStderr bool
	}{
		{name: "invalid document", input: `{`, wantExit: 1, wantStderr: true},
		{
			name:  "request failed",
			input: emptyWorkflowGraphDocumentJSON,
			remote: &workflowGraphApplyRemote{
				definition: workflowGraphApplyDefinition(t, 1),
				getError:   errors.New("get failed"),
			},
			wantExit: 1, wantStderr: true,
		},
		{
			name:  "blocked",
			input: emptyWorkflowGraphDocumentJSON,
			remote: &workflowGraphApplyRemote{
				definition: workflowGraphApplyDefinition(t, 1),
				previewResponse: workflowGraphApplyPreview(1, true, false, false, []serverapi.WorkflowGraphSaveBlocker{{
					Code: "validation_failed", Message: "invalid graph", Count: 1, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
				}}),
			},
			wantExit: 1, wantStderr: true,
		},
		{
			name:  "confirmation required",
			input: emptyWorkflowGraphDocumentJSON,
			remote: &workflowGraphApplyRemote{
				definition: workflowGraphApplyDefinition(t, 1),
				previewResponse: workflowGraphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{{
					Code: "confirmation_required", Message: "confirm removal", Count: 1, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
				}}),
			},
			wantExit: 1, wantStderr: true,
		},
		{
			name:       "unchanged",
			input:      emptyWorkflowGraphDocumentJSON,
			remote:     workflowGraphApplyUnchangedRemote(t, 1),
			wantExit:   0,
			wantStdout: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.remote != nil {
				installWorkflowCommandRemote(t, test.remote)
			}
			var stdout, stderr bytes.Buffer
			exitCode := workflowSubcommandWithInput(
				[]string{"graph", "apply", "-"},
				bytes.NewBufferString(test.input),
				&stdout,
				&stderr,
			)
			if exitCode != test.wantExit || (stdout.Len() > 0) != test.wantStdout || (stderr.Len() > 0) != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			if test.remote != nil && test.remote.saveCalls != 0 {
				t.Fatalf("save calls = %d, want zero", test.remote.saveCalls)
			}
		})
	}
}

func TestWorkflowGraphApplyUsageErrorsExitTwoWithoutJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := workflowSubcommandWithInput([]string{"graph", "apply", "--json"}, bytes.NewBuffer(nil), &stdout, &stderr)
	if exitCode != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestWorkflowGraphApplyReportsCloseAndJSONWriterFailures(t *testing.T) {
	t.Run("close after unchanged", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		remote.closeError = errors.New("close failed")
		installWorkflowCommandRemote(t, remote)
		var stdout, stderr bytes.Buffer
		exitCode := workflowSubcommandWithInput(
			[]string{"graph", "apply", "-", "--json"},
			bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
			&stdout,
			&stderr,
		)
		if exitCode != 0 || stdout.Len() == 0 || stderr.Len() == 0 || remote.closeCalls != 1 {
			t.Fatalf("exit=%d stdout=%q stderr=%q close=%d", exitCode, stdout.String(), stderr.String(), remote.closeCalls)
		}
	})

	t.Run("JSON writer", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		installWorkflowCommandRemote(t, remote)
		var stderr bytes.Buffer
		exitCode := workflowSubcommandWithInput(
			[]string{"graph", "apply", "-", "--json"},
			bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
			bindingMutationFailingWriter{},
			&stderr,
		)
		if exitCode != 1 || stderr.Len() == 0 || remote.saveCalls != 0 || remote.closeCalls != 1 {
			t.Fatalf("exit=%d stderr=%q save=%d close=%d", exitCode, stderr.String(), remote.saveCalls, remote.closeCalls)
		}
	})

	t.Run("human writer", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		installWorkflowCommandRemote(t, remote)
		var stderr bytes.Buffer
		exitCode := workflowSubcommandWithInput(
			[]string{"graph", "apply", "-"},
			bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
			bindingMutationFailingWriter{},
			&stderr,
		)
		if exitCode != 1 || stderr.Len() == 0 || remote.saveCalls != 0 || remote.closeCalls != 1 {
			t.Fatalf("exit=%d stderr=%q save=%d close=%d", exitCode, stderr.String(), remote.saveCalls, remote.closeCalls)
		}
	})
}
