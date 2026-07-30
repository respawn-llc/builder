package main

import (
	"context"
	"fmt"
	"io"

	"core/shared/config"
	"core/shared/serverapi"
)

func workflowDeleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" workflow delete", stderr, workflowDeleteUsage)
	confirm := fs.Bool("confirm", false, "delete using the current preview counts")
	jsonOut := fs.Bool("json", false, "write the deletion preview or result as JSON")
	positionals, ok, exitCode := parseWorkflowPositionals(fs, args, 1, stderr, "workflow delete requires <uuid>")
	if !ok {
		return exitCode
	}
	selector, err := parseWorkflowSelector(positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(_ config.App, remote workflowCommandRemote) int {
		ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
		defer cancel()
		workflowID := selector.value
		workflowDisplayID := selector.String()
		preview, err := remote.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if preview.Impact.WorkflowID != workflowID {
			fmt.Fprintf(stderr, "workflow deletion preview identity %q does not match requested workflow %q\n", preview.Impact.WorkflowID, workflowDisplayID)
			return 1
		}
		previewOutput, err := workflowDeleteResponseForCLI(serverapi.WorkflowDeleteResponse{Impact: preview.Impact})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !*confirm {
			if *jsonOut {
				if exitCode := writeCommandJSON(stdout, stderr, previewOutput); exitCode != 0 {
					return exitCode
				}
			} else {
				writeWorkflowDeleteImpact(stdout, previewOutput.Impact)
			}
			fmt.Fprintln(stderr, "Workflow deletion was not confirmed. Rerun with --confirm to delete it.")
			return 1
		}
		resp, err := remote.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
			WorkflowID:           workflowID,
			Confirmed:            true,
			ExpectedVersion:      preview.Impact.Version,
			ExpectedProjectCount: preview.Impact.ProjectCount,
			ExpectedLinkCount:    preview.Impact.LinkCount,
			ExpectedTaskCount:    preview.Impact.TaskCount,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if resp.Impact.WorkflowID != workflowID {
			fmt.Fprintf(stderr, "workflow deletion result identity %q does not match requested workflow %q\n", resp.Impact.WorkflowID, workflowDisplayID)
			return 1
		}
		if resp.Deleted == (len(resp.Blockers) > 0) {
			fmt.Fprintf(stderr, "workflow deletion returned inconsistent result: deleted=%t blockers=%d workflow=%q\n", resp.Deleted, len(resp.Blockers), workflowDisplayID)
			return 1
		}
		output, err := workflowDeleteResponseForCLI(resp)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOut {
			if exitCode := writeCommandJSON(stdout, stderr, output); exitCode != 0 {
				return exitCode
			}
		}
		if !output.Deleted {
			if !*jsonOut {
				writeWorkflowDeleteImpact(stdout, output.Impact)
				fmt.Fprintln(stderr, "Workflow was not deleted; resolve these blockers first:")
				for _, blocker := range output.Blockers {
					writeWorkflowBlockerLine(stderr, blocker.Code, blocker.Message, blocker.Count)
				}
			}
			return 1
		}
		if !*jsonOut {
			fmt.Fprintf(stdout, "Deleted workflow %s.\n", output.Impact.WorkflowID)
		}
		return 0
	})
}

func writeWorkflowDeleteImpact(w io.Writer, impact serverapi.WorkflowDeleteImpact) {
	fmt.Fprintf(w, "Workflow %s deletion impact at version %d:\n", impact.WorkflowID, impact.Version)
	fmt.Fprintf(w, "  Projects: %d\n", impact.ProjectCount)
	fmt.Fprintf(w, "  Project links: %d\n", impact.LinkCount)
	fmt.Fprintf(w, "  Projects requiring a replacement default: %d\n", impact.DefaultReplacementProjectCount)
	fmt.Fprintf(w, "  Tasks: %d\n", impact.TaskCount)
	fmt.Fprintf(w, "  Current nodes: %d\n", impact.CurrentNodeCount)
	fmt.Fprintf(w, "  Pending approvals: %d\n", impact.PendingApprovalCount)
	fmt.Fprintf(w, "  Blocked tasks: %d\n", impact.BlockedTaskCount)
}
