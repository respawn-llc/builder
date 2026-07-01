package commands

import (
	"strings"
	"testing"
)

func TestExecuteBuiltins(t *testing.T) {
	r := NewDefaultRegistry()
	if command, ok := r.Command("/name"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /name command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if command, ok := r.Command("/thinking"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /thinking command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if command, ok := r.Command("/fast"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /fast command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if command, ok := r.Command("/supervisor"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /supervisor command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if command, ok := r.Command("/autocompaction"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /autocompaction command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if command, ok := r.Command("/status"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /status command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if command, ok := r.Command("/copy"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /copy command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if command, ok := r.Command("/compact"); !ok || command.RunWhileBusy {
		t.Fatalf("expected /compact command to require idle, got %+v, ok=%v", command, ok)
	}
	if got := r.Execute("/new"); got.Action != ActionNew {
		t.Fatalf("expected ActionNew, got %+v", got)
	}
	if got := r.Execute("/resume"); got.Action != ActionResume {
		t.Fatalf("expected ActionResume, got %+v", got)
	}
	if got := r.Execute("/logout"); got.Action != ActionLogout {
		t.Fatalf("expected ActionLogout, got %+v", got)
	}
	if got := r.Execute("/login"); got.Action != ActionLogout {
		t.Fatalf("expected /login alias to map to ActionLogout, got %+v", got)
	}
	if got := r.Execute("/exit"); got.Action != ActionExit {
		t.Fatalf("expected ActionExit, got %+v", got)
	}
	if got := r.Execute("/compact"); got.Action != ActionCompact {
		t.Fatalf("expected ActionCompact, got %+v", got)
	}
	if got := r.Execute("/compact keep API details"); got.Action != ActionCompact || got.Args != "keep API details" {
		t.Fatalf("expected ActionCompact with args, got %+v", got)
	}
	if got := r.Execute("/name incident triage"); got.Action != ActionSetName || got.SessionName != "incident triage" {
		t.Fatalf("expected ActionSetName with title, got %+v", got)
	}
	if got := r.Execute("/name"); got.Action != ActionSetName || got.SessionName != "" {
		t.Fatalf("expected ActionSetName reset, got %+v", got)
	}
	if got := r.Execute("/thinking high"); got.Action != ActionSetThinking || got.ThinkingLevel != "high" {
		t.Fatalf("expected ActionSetThinking high, got %+v", got)
	}
	if got := r.Execute("/thinking"); got.Action != ActionSetThinking || got.ThinkingLevel != "" {
		t.Fatalf("expected ActionSetThinking show-current, got %+v", got)
	}
	if got := r.Execute("/fast"); got.Action != ActionSetFast || got.FastMode != "" {
		t.Fatalf("expected ActionSetFast toggle, got %+v", got)
	}
	if got := r.Execute("/fast on"); got.Action != ActionSetFast || got.FastMode != "on" {
		t.Fatalf("expected ActionSetFast on, got %+v", got)
	}
	if got := r.Execute("/fast off"); got.Action != ActionSetFast || got.FastMode != "off" {
		t.Fatalf("expected ActionSetFast off, got %+v", got)
	}
	if got := r.Execute("/fast status"); got.Action != ActionSetFast || got.FastMode != "status" {
		t.Fatalf("expected ActionSetFast status, got %+v", got)
	}
	if got := r.Execute("/supervisor"); got.Action != ActionSetSupervisor || got.SupervisorMode != "" {
		t.Fatalf("expected ActionSetSupervisor toggle, got %+v", got)
	}
	if got := r.Execute("/supervisor on"); got.Action != ActionSetSupervisor || got.SupervisorMode != "on" {
		t.Fatalf("expected ActionSetSupervisor on, got %+v", got)
	}
	if got := r.Execute("/supervisor off"); got.Action != ActionSetSupervisor || got.SupervisorMode != "off" {
		t.Fatalf("expected ActionSetSupervisor off, got %+v", got)
	}
	if got := r.Execute("/autocompaction"); got.Action != ActionSetAutoCompaction || got.AutoCompactionMode != "" {
		t.Fatalf("expected ActionSetAutoCompaction toggle, got %+v", got)
	}
	if got := r.Execute("/autocompaction on"); got.Action != ActionSetAutoCompaction || got.AutoCompactionMode != "on" {
		t.Fatalf("expected ActionSetAutoCompaction on, got %+v", got)
	}
	if got := r.Execute("/autocompaction off"); got.Action != ActionSetAutoCompaction || got.AutoCompactionMode != "off" {
		t.Fatalf("expected ActionSetAutoCompaction off, got %+v", got)
	}
	if got := r.Execute("/status"); got.Action != ActionStatus {
		t.Fatalf("expected ActionStatus, got %+v", got)
	}
	if command, ok := r.Command("/goal"); !ok || !command.RunWhileBusy {
		t.Fatalf("expected /goal command to be runnable while busy, got %+v, ok=%v", command, ok)
	}
	if got := r.Execute("/goal"); got.Action != ActionGoal || got.GoalMode != GoalModeShow {
		t.Fatalf("expected ActionGoal show, got %+v", got)
	}
	if got := r.Execute("/goal show"); got.Action != ActionGoal || got.GoalMode != GoalModeShow || got.GoalObjective != "" {
		t.Fatalf("expected ActionGoal show for explicit show, got %+v", got)
	}
	if got := r.Execute("/goal ship feature"); got.Action != ActionGoal || got.GoalMode != GoalModeSet || got.GoalObjective != "ship feature" {
		t.Fatalf("expected ActionGoal set, got %+v", got)
	}
	if got := r.Execute("/goal pause"); got.Action != ActionGoal || got.GoalMode != GoalModePause {
		t.Fatalf("expected ActionGoal pause, got %+v", got)
	}
	if got := r.Execute("/goal resume"); got.Action != ActionGoal || got.GoalMode != GoalModeResume {
		t.Fatalf("expected ActionGoal resume, got %+v", got)
	}
	if got := r.Execute("/goal clear"); got.Action != ActionGoal || got.GoalMode != GoalModeClear {
		t.Fatalf("expected ActionGoal clear, got %+v", got)
	}
	if got := r.Execute("/copy"); got.Action != ActionCopy {
		t.Fatalf("expected ActionCopy, got %+v", got)
	}
	if command, ok := r.Command("/wt"); !ok || command.Name != "worktree" || command.RunWhileBusy {
		t.Fatalf("expected /wt alias to resolve to /worktree, got %+v, ok=%v", command, ok)
	}
	if got := r.Execute("/wt list"); got.Action != ActionWorktree || got.Args != "list" {
		t.Fatalf("expected /wt alias to execute worktree action, got %+v", got)
	}
	if got := r.Execute("/back"); got.Action != ActionBack {
		t.Fatalf("expected ActionBack, got %+v", got)
	}
	got := r.Execute("/review src/cli/app")
	if !got.Handled || !got.SubmitUser {
		t.Fatalf("expected /review to submit a user prompt, got %+v", got)
	}
	if got.User == "" {
		t.Fatal("expected /review prompt payload")
	}
	if got.User == "/review src/cli/app" {
		t.Fatalf("expected injected prompt content, got %q", got.User)
	}
	if got.Action != ActionNone {
		t.Fatalf("expected /review action to be none, got %q", got.Action)
	}
	if !got.FreshConversation {
		t.Fatalf("expected /review to require fresh conversation, got %+v", got)
	}
	if got.Text != "" {
		t.Fatalf("expected /review to avoid system text, got %q", got.Text)
	}
	if got.Args != "" {
		t.Fatalf("expected /review args to be consumed by prompt payload, got %q", got.Args)
	}
	if !strings.HasSuffix(got.User, "src/cli/app") {
		t.Fatalf("expected /review args appended to prompt payload, got %q", got.User)
	}

	got = r.Execute("/init starter repo")
	if !got.Handled || !got.SubmitUser {
		t.Fatalf("expected /init to submit a user prompt, got %+v", got)
	}
	if got.User == "/init starter repo" {
		t.Fatalf("expected injected prompt content, got %q", got.User)
	}
	if got.Action != ActionNone {
		t.Fatalf("expected /init action to be none, got %q", got.Action)
	}
	if !got.FreshConversation {
		t.Fatalf("expected /init to require fresh conversation, got %+v", got)
	}
	if got.Text != "" {
		t.Fatalf("expected /init to avoid system text, got %q", got.Text)
	}
	if got.Args != "" {
		t.Fatalf("expected /init args to be consumed by prompt payload, got %q", got.Args)
	}
	if !strings.HasSuffix(got.User, "starter repo") {
		t.Fatalf("expected /init args appended to prompt payload, got %q", got.User)
	}
}

func TestExecuteUnknown(t *testing.T) {
	r := NewDefaultRegistry()
	if command, ok := r.Command("/nope"); ok || command.Name != "" {
		t.Fatalf("expected unknown command lookup miss, got %+v, ok=%v", command, ok)
	}
	got := r.Execute("/nope")
	if got.Handled {
		t.Fatal("expected unknown slash command to be unhandled")
	}
	if got.Action != ActionUnhandled {
		t.Fatalf("expected ActionUnhandled, got %q", got.Action)
	}
	if got.Text != "" {
		t.Fatalf("expected no system text for unknown command, got %q", got.Text)
	}
}

func TestMatchReturnsBestSubstringFirst(t *testing.T) {
	r := NewDefaultRegistry()
	matches := r.Match("o")
	if len(matches) < 2 {
		t.Fatalf("expected multiple matches, got %d", len(matches))
	}
	if matches[0].Name != "copy" {
		t.Fatalf("expected best match first, got %q", matches[0].Name)
	}
}

func TestHiddenAliasesDoNotAppearInVisibleCommandLists(t *testing.T) {
	r := NewDefaultRegistry()
	for _, command := range r.Commands() {
		if command.Name == "wt" {
			t.Fatal("expected /wt alias to stay hidden from visible command list")
		}
	}
	for _, command := range r.Match("wt") {
		if command.Name == "wt" {
			t.Fatal("expected /wt alias to stay hidden from visible match list")
		}
	}
}

func TestRegisterPanicsWhenNameContainsWhitespace(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for command name with whitespace")
		}
	}()
	r.RegisterWithOptions("bad name", "", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{}
	})
}
