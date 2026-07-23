package worktreeui

import (
	"testing"

	"core/shared/serverapi"
)

func TestScheduleIncrementsTokenAndDebouncesNonEmptyQuery(t *testing.T) {
	state, outcome := Schedule(State{
		ErrorText:     "old error",
		SubmitPending: true,
		Token:         7,
		Resolution:    serverapi.WorktreeCreateTargetResolution{Kind: serverapi.WorktreeCreateTargetResolutionKindExistingBranch},
	}, " main ")
	if state.Token != 8 {
		t.Fatalf("token = %d, want 8", state.Token)
	}
	if !state.Resolving {
		t.Fatal("expected resolving")
	}
	if state.SubmitPending {
		t.Fatal("submit pending should clear")
	}
	if state.ErrorText != "" {
		t.Fatalf("error text = %q, want empty", state.ErrorText)
	}
	if state.Resolution.Kind != "" {
		t.Fatalf("resolution kind = %q, want cleared", state.Resolution.Kind)
	}
	if outcome.Token != 8 || !outcome.Debounce {
		t.Fatalf("outcome = %+v, want token 8 debounce", outcome)
	}
}

func TestScheduleEmptyQueryClearsResolutionWithoutDebounce(t *testing.T) {
	state, outcome := Schedule(State{Token: 2, Resolution: serverapi.WorktreeCreateTargetResolution{Kind: serverapi.WorktreeCreateTargetResolutionKindNewBranch}}, " ")
	if state.Token != 3 {
		t.Fatalf("token = %d, want 3", state.Token)
	}
	if state.Resolving {
		t.Fatal("resolving should be false for empty query")
	}
	if outcome.Debounce {
		t.Fatal("empty query should not debounce")
	}
	if state.Resolution.Kind != "" {
		t.Fatalf("resolution kind = %q, want cleared", state.Resolution.Kind)
	}
}

func TestBeginSubmitValidatesAndMarksSubmitPending(t *testing.T) {
	state, outcome, err := BeginSubmit(State{Token: 4}, " feature ")
	if err != nil {
		t.Fatalf("BeginSubmit: %v", err)
	}
	if state.Token != 5 || !state.Resolving || !state.SubmitPending {
		t.Fatalf("state = %+v, want resolving submit-pending token 5", state)
	}
	if outcome.Token != 5 || outcome.Query != "feature" {
		t.Fatalf("outcome = %+v, want token 5 trimmed query", outcome)
	}
	state, _, err = BeginSubmit(State{ErrorText: "old"}, " ")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if state.ErrorText != "Branch or ref is required" {
		t.Fatalf("error text = %q, want validation text", state.ErrorText)
	}
}

func TestDebounceReadyClearsEmptyQueryAndIgnoresStaleToken(t *testing.T) {
	initial := State{Token: 2, Resolving: true, SubmitPending: true, ErrorText: "old", Resolution: serverapi.WorktreeCreateTargetResolution{Kind: serverapi.WorktreeCreateTargetResolutionKindExistingBranch}}
	state, outcome := DebounceReady(initial, 1, "main")
	if !outcome.Ignored {
		t.Fatal("expected stale token ignored")
	}
	if state != initial {
		t.Fatalf("state changed for stale token: %+v", state)
	}
	state, outcome = DebounceReady(initial, 2, " ")
	if outcome.Ignored || outcome.Start {
		t.Fatalf("outcome = %+v, want handled without start", outcome)
	}
	if state.Resolving || state.SubmitPending || state.ErrorText != "" || state.Resolution.Kind != "" {
		t.Fatalf("state = %+v, want cleared empty query state", state)
	}
}

func TestDoneIgnoresStaleResponsesAndSubmitsPendingSuccess(t *testing.T) {
	initial := State{Token: 3, Resolving: true, SubmitPending: true}
	state, outcome := Done(initial, DoneInput{Token: 2, CurrentQuery: "main", ResponseQuery: "main"})
	if !outcome.Ignored {
		t.Fatal("expected stale token ignored")
	}
	if state != initial {
		t.Fatalf("state changed for stale token: %+v", state)
	}
	state, outcome = Done(initial, DoneInput{Token: 3, CurrentQuery: "main", ResponseQuery: "other"})
	if !outcome.Ignored {
		t.Fatal("expected stale query ignored")
	}
	if state != initial {
		t.Fatalf("state changed for stale query: %+v", state)
	}
	resolution := serverapi.WorktreeCreateTargetResolution{Kind: serverapi.WorktreeCreateTargetResolutionKindExistingBranch}
	state, outcome = Done(initial, DoneInput{Token: 3, CurrentQuery: "main", ResponseQuery: "main", Resolution: resolution})
	if outcome.Ignored || !outcome.Submit || outcome.SubmitKind != resolution.Kind {
		t.Fatalf("outcome = %+v, want submit existing branch", outcome)
	}
	if state.Resolving || state.SubmitPending || state.Resolution.Kind != resolution.Kind {
		t.Fatalf("state = %+v, want resolved non-pending", state)
	}
}

func TestDoneStoresFormattedError(t *testing.T) {
	state, outcome := Done(State{Token: 1, Resolving: true, SubmitPending: true}, DoneInput{
		Token:         1,
		CurrentQuery:  "main",
		ResponseQuery: "main",
		HasError:      true,
		ErrorText:     "formatted error",
		Resolution:    serverapi.WorktreeCreateTargetResolution{Kind: serverapi.WorktreeCreateTargetResolutionKindExistingBranch},
	})
	if outcome.Submit || outcome.Ignored {
		t.Fatalf("outcome = %+v, want handled error without submit", outcome)
	}
	if state.Resolving || state.SubmitPending || state.ErrorText != "formatted error" || state.Resolution.Kind != "" {
		t.Fatalf("state = %+v, want formatted error and cleared resolution", state)
	}
}
