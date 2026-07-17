package commands

import (
	"strings"
	"testing"
)

func TestExecuteBuiltins(t *testing.T) {
	registry := NewDefaultRegistry()
	for input, want := range map[string]Result{
		"/new":                      {Handled: true, Action: ActionNew},
		"/resume":                   {Handled: true, Action: ActionResume},
		"/logout":                   {Handled: true, Action: ActionLogout},
		"/login":                    {Handled: true, Action: ActionLogout},
		"/exit":                     {Handled: true, Action: ActionExit},
		"/compact":                  {Handled: true, Action: ActionCompact},
		"/compact keep API details": {Handled: true, Action: ActionCompact, Args: "keep API details"},
		"/name incident triage":     {Handled: true, Action: ActionSetName, SessionName: "incident triage"},
		"/name":                     {Handled: true, Action: ActionSetName},
		"/thinking HIGH":            {Handled: true, Action: ActionSetThinking, ThinkingLevel: "high"},
		"/thinking":                 {Handled: true, Action: ActionSetThinking},
		"/fast":                     {Handled: true, Action: ActionSetFast},
		"/fast ON":                  {Handled: true, Action: ActionSetFast, FastMode: "on"},
		"/fast off":                 {Handled: true, Action: ActionSetFast, FastMode: "off"},
		"/fast status":              {Handled: true, Action: ActionSetFast, FastMode: "status"},
		"/supervisor":               {Handled: true, Action: ActionSetSupervisor},
		"/supervisor ON":            {Handled: true, Action: ActionSetSupervisor, SupervisorMode: "on"},
		"/supervisor off":           {Handled: true, Action: ActionSetSupervisor, SupervisorMode: "off"},
		"/autocompaction":           {Handled: true, Action: ActionSetAutoCompaction},
		"/autocompaction ON":        {Handled: true, Action: ActionSetAutoCompaction, AutoCompactionMode: "on"},
		"/autocompaction off":       {Handled: true, Action: ActionSetAutoCompaction, AutoCompactionMode: "off"},
		"/questions":                {Handled: true, Action: ActionSetQuestions},
		"/questions ON":             {Handled: true, Action: ActionSetQuestions, QuestionsMode: "on"},
		"/questions off":            {Handled: true, Action: ActionSetQuestions, QuestionsMode: "off"},
		"/status":                   {Handled: true, Action: ActionStatus},
		"/goal":                     {Handled: true, Action: ActionGoal, GoalMode: GoalModeShow},
		"/goal show":                {Handled: true, Action: ActionGoal, GoalMode: GoalModeShow},
		"/goal ship feature":        {Handled: true, Action: ActionGoal, GoalMode: GoalModeSet, GoalObjective: "ship feature"},
		"/goal pause":               {Handled: true, Action: ActionGoal, GoalMode: GoalModePause},
		"/goal resume":              {Handled: true, Action: ActionGoal, GoalMode: GoalModeResume},
		"/goal clear":               {Handled: true, Action: ActionGoal, GoalMode: GoalModeClear},
		"/ps logs process-1":        {Handled: true, Action: ActionProcesses, Args: "logs process-1"},
		"/worktree list":            {Handled: true, Action: ActionWorktree, Args: "list"},
		"/wt list":                  {Handled: true, Action: ActionWorktree, Args: "list"},
		"/copy":                     {Handled: true, Action: ActionCopy},
		"/back":                     {Handled: true, Action: ActionBack},
	} {
		if got := registry.Execute(input); got != want {
			t.Fatalf("Execute(%q) = %+v, want %+v", input, got, want)
		}
	}
}

func TestExecuteBuiltinPromptCommandsSubmitFreshUserTurns(t *testing.T) {
	registry := NewDefaultRegistry()
	for input, suffix := range map[string]string{
		"/review src/cli/app": "src/cli/app",
		"/init starter repo":  "starter repo",
	} {
		got := registry.Execute(input)
		if !got.Handled || !got.SubmitUser || got.Action != ActionNone || !got.FreshConversation {
			t.Fatalf("Execute(%q) = %+v, want fresh user submission", input, got)
		}
		if got.User == "" || got.User == input || !strings.HasSuffix(got.User, suffix) {
			t.Fatalf("Execute(%q) user payload = %q, want injected prompt ending in %q", input, got.User, suffix)
		}
		if got.Text != "" || got.Args != "" {
			t.Fatalf("Execute(%q) leaked system text or args: %+v", input, got)
		}
	}
}

func TestExecuteUnknown(t *testing.T) {
	registry := NewDefaultRegistry()
	if command, ok := registry.Command("/nope"); ok || command != (Command{}) {
		t.Fatalf("unknown command lookup = %+v, ok=%v", command, ok)
	}
	if got := registry.Execute("/nope"); got != (Result{Action: ActionUnhandled}) {
		t.Fatalf("unknown command result = %+v", got)
	}
}

func TestCommandDiscoveryOrdersMatchesAndHidesAliases(t *testing.T) {
	registry := NewDefaultRegistry()
	matches := registry.Match("o")
	if len(matches) < 2 || matches[0].Name != "copy" {
		t.Fatalf("matches = %+v, want copy first", matches)
	}
	for _, commands := range [][]Command{registry.Commands(), registry.Match("wt")} {
		for _, command := range commands {
			if command.Name == "wt" {
				t.Fatal("worktree alias must stay hidden from visible command lists")
			}
		}
	}
	if command, ok := registry.Command("/wt"); !ok || command.Name != "worktree" || command.ActiveRunPolicy != ActiveRunPolicyRequiresIdle {
		t.Fatalf("worktree alias lookup = %+v, ok=%v", command, ok)
	}
}

func TestRegisterPanicsWhenNameContainsWhitespace(t *testing.T) {
	registry := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for command name with whitespace")
		}
	}()
	registry.RegisterWithOptions("bad name", "", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{}
	})
}
