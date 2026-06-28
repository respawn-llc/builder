package status

import (
	"context"
	"testing"

	"core/shared/clientui"
)

func TestExecutionTargetPrefersExplicitRequestTargetOverRuntimeView(t *testing.T) {
	runtimeTarget := clientui.SessionExecutionTarget{WorktreeRoot: "/old", EffectiveWorkdir: "/old/pkg"}
	explicitTarget := clientui.SessionExecutionTarget{WorktreeRoot: "/new", EffectiveWorkdir: "/new/pkg"}

	got := ExecutionTarget(Request{
		Runtime:         stubRuntimeClient{target: runtimeTarget},
		ExecutionTarget: explicitTarget,
	})

	if !clientui.SessionExecutionTargetsEqual(got, explicitTarget) {
		t.Fatalf("execution target = %+v, want explicit %+v", got, explicitTarget)
	}
}

func TestExecutionTargetFallsBackToRuntimeViewWhenExplicitTargetIsEmpty(t *testing.T) {
	runtimeTarget := clientui.SessionExecutionTarget{WorktreeRoot: "/runtime", EffectiveWorkdir: "/runtime/pkg"}

	got := ExecutionTarget(Request{Runtime: stubRuntimeClient{target: runtimeTarget}})

	if !clientui.SessionExecutionTargetsEqual(got, runtimeTarget) {
		t.Fatalf("execution target = %+v, want runtime %+v", got, runtimeTarget)
	}
}

type stubRuntimeClient struct {
	target clientui.SessionExecutionTarget
}

func (s stubRuntimeClient) MainView() clientui.RuntimeMainView {
	return clientui.RuntimeMainView{Session: s.SessionView()}
}

func (s stubRuntimeClient) RefreshMainView() (clientui.RuntimeMainView, error) {
	return s.MainView(), nil
}

func (s stubRuntimeClient) Transcript() clientui.TranscriptPage { return clientui.TranscriptPage{} }

func (s stubRuntimeClient) RefreshTranscript() (clientui.TranscriptPage, error) {
	return clientui.TranscriptPage{}, nil
}

func (s stubRuntimeClient) RefreshTranscriptPage(clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return clientui.TranscriptPage{}, nil
}

func (s stubRuntimeClient) LoadTranscriptPage(clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return clientui.TranscriptPage{}, nil
}

func (s stubRuntimeClient) Status() clientui.RuntimeStatus { return clientui.RuntimeStatus{} }

func (s stubRuntimeClient) SessionView() clientui.RuntimeSessionView {
	return clientui.RuntimeSessionView{ExecutionTarget: s.target}
}

func (s stubRuntimeClient) SetSessionName(string) error { return nil }

func (s stubRuntimeClient) SetThinkingLevel(string) error { return nil }

func (s stubRuntimeClient) SetFastModeEnabled(bool) (bool, error) { return false, nil }

func (s stubRuntimeClient) SetReviewerEnabled(bool) (bool, string, error) {
	return false, "", nil
}

func (s stubRuntimeClient) SetAutoCompactionEnabled(bool) (bool, bool, error) {
	return false, false, nil
}

func (s stubRuntimeClient) SetQuestionsEnabled(bool) (bool, error) {
	return false, nil
}

func (s stubRuntimeClient) ShowGoal() (*clientui.RuntimeGoal, error) { return nil, nil }

func (s stubRuntimeClient) SetGoal(string) (*clientui.RuntimeGoal, error) { return nil, nil }

func (s stubRuntimeClient) PauseGoal() (*clientui.RuntimeGoal, error) { return nil, nil }

func (s stubRuntimeClient) ResumeGoal() (*clientui.RuntimeGoal, error) { return nil, nil }

func (s stubRuntimeClient) ClearGoal() (*clientui.RuntimeGoal, error) { return nil, nil }

func (s stubRuntimeClient) AppendCommittedEntry(string, string) error { return nil }
func (s stubRuntimeClient) AppendCommittedEntryWithNoticeID(string, string, string) error {
	return nil
}

func (s stubRuntimeClient) SubmitUserMessage(context.Context, string) (clientui.UserTurnSubmission, error) {
	return clientui.UserTurnSubmission{}, nil
}

func (s stubRuntimeClient) SubmitUserShellCommand(context.Context, string) error { return nil }

func (s stubRuntimeClient) CompactContext(context.Context, string) error { return nil }

func (s stubRuntimeClient) HasQueuedUserWork() (bool, error) { return false, nil }

func (s stubRuntimeClient) SubmitQueuedUserMessages(context.Context) (string, error) {
	return "", nil
}

func (s stubRuntimeClient) Interrupt() error { return nil }

func (s stubRuntimeClient) QueueUserMessage(string) (clientui.QueuedUserMessage, error) {
	return clientui.QueuedUserMessage{}, nil
}

func (s stubRuntimeClient) DiscardQueuedUserMessage(string) bool { return false }

func (s stubRuntimeClient) RecordPromptHistory(string) error { return nil }
