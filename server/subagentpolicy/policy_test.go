package subagentpolicy

import (
	"errors"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

func TestAuthorizeCallerTargetMatrix(t *testing.T) {
	settings := config.Settings{
		Workflow: config.WorkflowSettings{Subagents: true},
		Subagents: map[string]config.SubagentRole{
			"worker":  {AgentCallableSet: true, AgentCallable: true, WorkflowSubagentSet: true, WorkflowSubagent: true},
			"hidden":  {AgentCallableSet: true, AgentCallable: true, WorkflowSubagentSet: true, WorkflowSubagent: false},
			"blocked": {AgentCallableSet: true, AgentCallable: false},
		},
	}
	workflow := &Caller{Workflow: true}
	for _, target := range []Target{
		{Kind: TargetOmittedBase},
		{Kind: TargetExplicitBase},
		{Kind: TargetNamed, Selector: "worker"},
		{Kind: TargetNamed, Selector: config.BuiltInSubagentRoleFast},
	} {
		if err := Authorize(settings, workflow, target); err != nil {
			t.Fatalf("Authorize workflow target %+v: %v", target, err)
		}
	}
	for _, selector := range []string{"hidden", "blocked"} {
		err := Authorize(settings, workflow, Target{Kind: TargetNamed, Selector: selector})
		var denied *serverapi.SubagentLaunchDeniedError
		if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialNotCallable {
			t.Fatalf("Authorize %q error = %T %v, want not-callable denial", selector, err, err)
		}
	}
	if err := Authorize(settings, nil, Target{Kind: TargetNamed, Selector: "blocked"}); err != nil {
		t.Fatalf("human blocked role should bypass callability: %v", err)
	}
	if err := Authorize(settings, nil, Target{Kind: TargetNamed, Selector: "missing"}); !isDenialKind(err, serverapi.SubagentLaunchDenialTargetMissing) {
		t.Fatalf("human missing role error = %v, want missing-target denial", err)
	}
	ordinary := &Caller{}
	if err := Authorize(settings, ordinary, Target{Kind: TargetNamed, Selector: "blocked"}); !isDenialKind(err, serverapi.SubagentLaunchDenialNotCallable) {
		t.Fatalf("ordinary blocked role error = %v, want not-callable denial", err)
	}
	for _, selector := range []string{config.BuiltInSubagentRoleFast, "worker"} {
		if err := Authorize(settings, ordinary, Target{Kind: TargetNamed, Selector: selector}); err != nil {
			t.Fatalf("ordinary caller launching %q: %v", selector, err)
		}
	}
	workflowNoSwitch := &Caller{Workflow: true}
	if err := Authorize(config.Settings{Subagents: settings.Subagents}, workflowNoSwitch, Target{Kind: TargetNamed, Selector: config.BuiltInSubagentRoleFast}); err != nil {
		t.Fatalf("fast target should bypass workflow controls: %v", err)
	}
	blockedFast := config.Settings{Subagents: map[string]config.SubagentRole{
		config.BuiltInSubagentRoleFast: {AgentCallableSet: true, AgentCallable: false},
	}}
	workflowBlockedFast := &Caller{Workflow: true}
	if err := Authorize(blockedFast, workflowBlockedFast, Target{Kind: TargetNamed, Selector: config.BuiltInSubagentRoleFast}); !isDenialKind(err, serverapi.SubagentLaunchDenialNotCallable) {
		t.Fatalf("blocked fast role error = %v, want not-callable denial", err)
	}
}

func isDenialKind(err error, kind serverapi.SubagentLaunchDenialKind) bool {
	var denied *serverapi.SubagentLaunchDeniedError
	return errors.As(err, &denied) && denied.Kind == kind
}
