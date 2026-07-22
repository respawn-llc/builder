package tools

import (
	"reflect"
	"testing"
)

func TestParseSimpleCommandAllowsEmptyQuotedArgumentAfterCommandName(t *testing.T) {
	args, ok := ParseSimpleShellCommand("printf ''")
	if !ok {
		t.Fatal("expected empty quoted arg to parse")
	}
	if len(args) != 2 || args[0] != "printf" || args[1] != "" {
		t.Fatalf("args = %#v, want [printf \"\"]", args)
	}
}

func TestParseSimpleCommandRejectsLeadingEnvAssignments(t *testing.T) {
	if _, ok := ParseSimpleShellCommand("FOO=bar go test ./..."); ok {
		t.Fatal("expected leading env assignment command to stay unsupported")
	}
}

func TestExtractLiteralShellInvocationsTraversesStructureAndExactWrappers(t *testing.T) {
	got := ExtractLiteralShellInvocations(
		`printf ready && command -- git worktree list; env KENT_MODE=test git worktree add feature`,
	)
	want := []LiteralShellInvocation{
		{Args: []string{"printf", "ready"}},
		{Args: []string{"git", "worktree", "list"}},
		{Args: []string{"git", "worktree", "add", "feature"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invocations = %#v, want %#v", got, want)
	}
}

func TestExtractLiteralShellInvocationsSkipsDynamicAndNonWrapperForms(t *testing.T) {
	got := ExtractLiteralShellInvocations(
		`git "$subcommand"; command -v git; env -i git worktree list; mycommand git worktree list`,
	)
	want := []LiteralShellInvocation{{Args: []string{"mycommand", "git", "worktree", "list"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invocations = %#v, want %#v", got, want)
	}
}

func TestParseSimpleShellCommandUnwrapsLiteralCommandAndEnv(t *testing.T) {
	for _, command := range []string{
		`command git worktree list`,
		`env KENT_MODE=test -- git worktree list`,
	} {
		got, ok := ParseSimpleShellCommand(command)
		if !ok || !reflect.DeepEqual(got, []string{"git", "worktree", "list"}) {
			t.Fatalf("ParseSimpleShellCommand(%q) = %#v, %t", command, got, ok)
		}
	}
}
