package serverapi

import (
	"errors"
	"testing"
)

func TestSessionRetargetErrorRoundTripsStructuredFacts(t *testing.T) {
	for _, reason := range []SessionRetargetErrorReason{
		SessionRetargetTargetProjectRequired,
		SessionRetargetTargetProjectConflict,
	} {
		t.Run(string(reason), func(t *testing.T) {
			source := &SessionRetargetError{
				Reason:        reason,
				SessionID:     "session-1",
				SourceProject: ProjectReference{ID: "project-source", Name: "Source"},
				TargetRoot:    "/work/target",
				CandidateProjects: []ProjectReference{
					{ID: "project-z", Name: "Zulu"},
					{ID: "project-a", Name: "Alpha"},
				},
			}
			decodedErr := DecodeSessionRetargetError(source.RPCErrorData(), source.Error())
			var decoded *SessionRetargetError
			if !errors.As(decodedErr, &decoded) {
				t.Fatalf("decoded error = %T %v, want SessionRetargetError", decodedErr, decodedErr)
			}
			if decoded.Reason != source.Reason ||
				decoded.SessionID != source.SessionID ||
				decoded.SourceProject != source.SourceProject ||
				decoded.TargetRoot != source.TargetRoot {
				t.Fatalf("decoded error = %+v, want facts from %+v", decoded, source)
			}
			if len(decoded.CandidateProjects) != 2 ||
				decoded.CandidateProjects[0].ID != "project-a" ||
				decoded.CandidateProjects[1].ID != "project-z" {
				t.Fatalf("decoded candidate projects = %+v, want stable ID order", decoded.CandidateProjects)
			}
		})
	}
}

func TestSessionRetargetWorktreeErrorsRoundTrip(t *testing.T) {
	for _, reason := range []SessionRetargetErrorReason{
		SessionRetargetSourceWorktree,
		SessionRetargetTargetWorktree,
	} {
		source := &SessionRetargetError{
			Reason:        reason,
			SessionID:     "session-1",
			SourceProject: ProjectReference{ID: "project-1", Name: "Project"},
			TargetRoot:    "/workspace",
		}
		if err := source.Validate(); err != nil {
			t.Fatalf("%s Validate: %v", reason, err)
		}
		decodedErr := DecodeSessionRetargetError(source.RPCErrorData(), source.Error())
		var decoded *SessionRetargetError
		if !errors.As(decodedErr, &decoded) || decoded.Reason != reason {
			t.Fatalf("%s decoded error = %#v", reason, decodedErr)
		}
	}
}
