package promptcommands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/prompts"
	"core/shared/runtimeinput"
	"core/shared/textutil"
)

func TestCatalogUsesOrderedRootsSortedEntriesAndNormalizedFirstValidPrecedence(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()

	writePromptFile(t, filepath.Join(workspaceRoot, ".kent", "prompts", "Review Plan.md"), "workspace")
	writePromptFile(t, filepath.Join(workspaceRoot, ".kent", "commands", "review_plan.md"), "shadowed")
	writePromptFile(t, filepath.Join(persistenceRoot, "prompts", "review_plan.md"), "global")
	writePromptFile(t, filepath.Join(persistenceRoot, "commands", "Alpha.md"), "alpha")
	writePromptFile(t, filepath.Join(persistenceRoot, "commands", "review.md"), "file shadow")
	writePromptFile(t, filepath.Join(persistenceRoot, ".generated", "prompts", "zeta.md"), "zeta")
	writePromptFile(t, filepath.Join(persistenceRoot, ".generated", "commands", "generated.md"), "generated")
	writePromptFile(t, filepath.Join(persistenceRoot, "commands", "nested", "ignored.md"), "ignored")
	writePromptFile(t, filepath.Join(persistenceRoot, "commands", "not-markdown.txt"), "ignored")

	entries, err := New(persistenceRoot, workspaceRoot).Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name+"="+entry.Preview)
	}
	want := []string{
		"prompt:review_plan=workspace",
		"prompt:alpha=alpha",
		"prompt:zeta=zeta",
		"prompt:generated=generated",
		"prompt:review=" + preview(prompts.ReviewPrompt),
		"prompt:init=" + preview(prompts.InitPrompt),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
}

func TestCatalogBlankHigherPrecedenceFallsBackAndPreviewCollapsesWhitespaceAtUnicodeLimit(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	writePromptFile(t, filepath.Join(workspaceRoot, ".kent", "prompts", "same.md"), " \n\t ")

	body := strings.Repeat("界", 256) + " trailing"
	writePromptFile(t, filepath.Join(persistenceRoot, "prompts", "same.md"), "fallback")
	writePromptFile(t, filepath.Join(persistenceRoot, "commands", "preview.md"), "first\n\n second\t third "+body)

	entries, err := New(persistenceRoot, workspaceRoot).Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if entries[0].Name != "prompt:same" || entries[0].Preview != "fallback" {
		t.Fatalf("fallback entry = %+v", entries[0])
	}
	if entries[1].Name != "prompt:preview" {
		t.Fatalf("preview entry = %+v", entries[1])
	}
	if got := []rune(entries[1].Preview); len(got) != 256 {
		t.Fatalf("preview rune length = %d, want 256", len(got))
	}
	if entries[1].Preview[:len("first second third ")] != "first second third " {
		t.Fatalf("preview = %q", entries[1].Preview)
	}
}

func TestResolveReadsCurrentWinningContentAndExpandsArguments(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	path := filepath.Join(persistenceRoot, "prompts", "review_file.md")
	writePromptFile(t, path, "before $ARGUMENTS")

	service := New(persistenceRoot, workspaceRoot)
	if got, err := service.Resolve("prompt:review_file", "  src/internal  "); err != nil || got != "before src/internal" {
		t.Fatalf("Resolve first = %q, %v", got, err)
	}
	writePromptFile(t, path, "after")
	if got, err := service.Resolve("prompt:review_file", "retry"); err != nil || got != "after\n\nretry" {
		t.Fatalf("Resolve current = %q, %v", got, err)
	}
}

func TestBuiltInPromptCommandsResolveThroughTheServerService(t *testing.T) {
	service := New(t.TempDir(), t.TempDir())
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "prompt:review", body: prompts.ReviewPrompt},
		{name: "prompt:init", body: prompts.InitPrompt},
	} {
		got, err := service.Resolve(test.name, "  src/internal  ")
		if err != nil {
			t.Fatalf("Resolve(%q): %v", test.name, err)
		}
		want := textutil.ExpandPromptTemplate(test.body, "  src/internal  ")
		if got != want {
			t.Fatalf("Resolve(%q) = %q, want %q", test.name, got, want)
		}
	}
}

func TestResolveReportsTypedRedactedErrors(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	service := New(persistenceRoot, workspaceRoot)

	_, err := service.Resolve("prompt:missing", "")
	var commandErr *Error
	if !errors.As(err, &commandErr) || commandErr.Kind != ErrorKindCommandNotFound {
		t.Fatalf("Resolve missing error = %T %v", err, err)
	}
	if strings.Contains(err.Error(), persistenceRoot) || strings.Contains(err.Error(), workspaceRoot) {
		t.Fatalf("error exposes source path: %v", err)
	}
}

func TestResolveDoesNotReadUnrelatedUnreadablePrompt(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	writePromptFile(t, filepath.Join(workspaceRoot, ".kent", "prompts", "good.md"), "good")
	bad := filepath.Join(workspaceRoot, ".kent", "prompts", "bad.md")
	writePromptFile(t, bad, "unreadable")
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	if got, err := New(persistenceRoot, workspaceRoot).Resolve("prompt:good", ""); err != nil || got != "good" {
		t.Fatalf("Resolve valid command = %q, %v", got, err)
	}
}

func TestCatalogReadFailureIsTypedAndRedacted(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	blocked := filepath.Join(workspaceRoot, ".kent", "prompts")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	_, err := New(persistenceRoot, workspaceRoot).Catalog()
	var commandErr *Error
	if !errors.As(err, &commandErr) || commandErr.Kind != ErrorKindCatalogRead {
		t.Fatalf("Catalog error = %T %v", err, err)
	}
	if strings.Contains(err.Error(), workspaceRoot) {
		t.Fatalf("error exposes source path: %v", err)
	}
}

func TestCatalogSkipsBrokenBuiltInShadowFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".kent", "prompts")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{
		runtimeinput.PromptCommandReviewIdentifier,
		runtimeinput.PromptCommandInitIdentifier,
	} {
		if err := os.Symlink("missing.md", filepath.Join(root, identifier+".md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	entries, err := New(t.TempDir(), filepath.Dir(filepath.Dir(root))).Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, name := range []string{
		runtimeinput.PromptCommandReviewName,
		runtimeinput.PromptCommandInitName,
	} {
		if !seen[name] {
			t.Fatalf("catalog omitted built-in %q: %+v", name, entries)
		}
	}
}

func TestNilErrorReceiverFailsExplicitly(t *testing.T) {
	var commandErr *Error
	defer func() {
		if recover() == nil {
			t.Fatal("nil error receiver did not panic")
		}
	}()
	_ = commandErr.Error()
}

func writePromptFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
