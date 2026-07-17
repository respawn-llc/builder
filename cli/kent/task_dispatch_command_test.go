package main

import (
	"strings"
	"testing"
)

func TestTaskRootDispatchRoutesCanonicalAndHiddenPluralAlias(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Task Alias Workflow")
	taskOut, taskErr, code := runRootCommand("task", "create", "--title", "Alias Task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	if code != 0 {
		t.Fatalf("kent task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)

	if _, stderr, code := runRootCommand("task", "show", "--project", binding.ProjectID, shortID); code != 0 {
		t.Fatalf("kent task show exit=%d stderr=%q", code, stderr)
	}
	if _, stderr, code := runRootCommand("tasks", "show", "--project", binding.ProjectID, shortID); code != 0 {
		t.Fatalf("kent tasks show exit=%d stderr=%q", code, stderr)
	}
	if _, stderr, code := runRootCommand("tasks", "comments", "list", "--project", binding.ProjectID, shortID); code != 0 {
		t.Fatalf("kent tasks comments list exit=%d stderr=%q", code, stderr)
	}
}

func TestTaskRootDispatchDoesNotDocumentHiddenPluralAlias(t *testing.T) {
	_, rootHelp, rootCode := runRootCommand("--help")
	if rootCode != 0 {
		t.Fatalf("kent --help exit=%d stderr=%q", rootCode, rootHelp)
	}
	if strings.Contains(rootHelp, "kent tasks") {
		t.Fatalf("root help documents hidden plural command:\n%s", rootHelp)
	}

	_, taskHelp, taskCode := runRootCommand("tasks", "--help")
	if taskCode != 0 {
		t.Fatalf("kent tasks --help exit=%d stderr=%q", taskCode, taskHelp)
	}
	if !strings.Contains(taskHelp, "Usage of kent task") {
		t.Fatalf("task help = %q, want canonical kent task usage", taskHelp)
	}
	if strings.Contains(taskHelp, "kent tasks") {
		t.Fatalf("task help documents hidden plural command:\n%s", taskHelp)
	}
}
