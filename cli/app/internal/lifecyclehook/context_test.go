package lifecyclehook_test

import (
	"errors"
	"testing"
	"time"

	"core/cli/app/internal/lifecyclehook"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestInitialContextUsesTypedSessionTitleAbsence(t *testing.T) {
	sessionID := runtimeids.NewSessionID()

	absent, err := lifecyclehook.InitialContext(sessionID.String(), nil)
	if err != nil {
		t.Fatalf("InitialContext absent title: %v", err)
	}
	if absent.SessionTitle != nil {
		t.Fatalf("absent session title = %v, want nil", absent.SessionTitle)
	}

	for name, invalid := range map[string]string{"empty": "", "blank": " \t "} {
		invalid := invalid
		t.Run(name, func(t *testing.T) {
			if _, err := lifecyclehook.InitialContext(sessionID.String(), &invalid); err == nil {
				t.Fatal("InitialContext accepted a present empty or blank title")
			}
		})
	}

	title := "  Incident triage  "
	present, err := lifecyclehook.InitialContext(sessionID.String(), &title)
	if err != nil {
		t.Fatalf("InitialContext present title: %v", err)
	}
	if present.SessionTitle == nil || *present.SessionTitle != title {
		t.Fatalf("present session title = %v, want preserved input", present.SessionTitle)
	}
}

func TestEventContextRejectsMalformedObservedFactsWithoutMutating(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	title := "Initial"
	initial, err := lifecyclehook.InitialContext(sessionID.String(), &title)
	if err != nil {
		t.Fatalf("InitialContext: %v", err)
	}
	context := lifecyclehook.NewEventContext(initial)

	if err := context.AcceptSessionIdentity(clientui.TranscriptSessionIdentity{}); err == nil {
		t.Fatal("AcceptSessionIdentity accepted a malformed fact")
	}
	if err := context.AcceptSessionStatus(clientui.TranscriptSessionStatus{}); err == nil {
		t.Fatal("AcceptSessionStatus accepted a malformed fact")
	}

	got := context.Snapshot()
	if got.SessionID == nil || *got.SessionID != sessionID ||
		got.SessionTitle == nil || *got.SessionTitle != title ||
		got.WorkflowTaskID != nil {
		t.Fatalf("malformed facts mutated lifecycle context: %+v", got)
	}
}

func TestDispatcherSurfacesObservationValidationIssues(t *testing.T) {
	dispatcher := lifecyclehook.New(t.Context(), []string{"unused"})
	t.Cleanup(dispatcher.Close)
	cause := errors.New("malformed session identity")
	dispatcher.Report(lifecyclehook.NewObservationIssue(
		lifecyclehook.ObservationFactSessionIdentity,
		cause,
	))

	issue := waitForDispatcherIssue(t, dispatcher.Issues(), 3*time.Second)
	observation, ok := issue.Detail.(lifecyclehook.ObservationIssue)
	if !ok {
		t.Fatalf("issue detail = %T, want observation issue", issue.Detail)
	}
	if observation.Fact != lifecyclehook.ObservationFactSessionIdentity ||
		observation.Cause == nil {
		t.Fatalf("observation issue = %+v", observation)
	}
}
