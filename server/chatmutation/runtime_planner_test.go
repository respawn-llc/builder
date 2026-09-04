package chatmutation

import (
	"context"
	"testing"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type runtimePlannerFixture struct {
	authority  *sessionruntime.Authority
	runtimeAPI *sessionruntime.API
	sessionID  runtimeids.SessionID
	workspace  string
}

type runtimePlannerClient struct{}

func (runtimePlannerClient) Generate(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	return llm.Response{}, nil
}

func (runtimePlannerClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func newRuntimePlannerFixture(t *testing.T) runtimePlannerFixture {
	t.Helper()
	persistence := sessiontest.NewPersistence()
	workspace := t.TempDir()
	store, err := session.Create(
		t.TempDir(),
		"sessions",
		workspace,
		sessioncontract.SessionCategoryMain,
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    persistence.Options(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close Runtime authority: %v", err)
		}
	})
	return runtimePlannerFixture{
		authority:  authority,
		runtimeAPI: sessionruntime.NewAPI(nil, authority, sessionruntime.APIOptions{}),
		sessionID:  sessionID,
		workspace:  workspace,
	}
}

func (f runtimePlannerFixture) runtimePlan(t *testing.T) sessionruntime.AgentRuntimePlan {
	t.Helper()
	filesystem, err := runtimewire.NewFilesystemContext(
		f.workspace,
		f.workspace,
		metadata.ProjectWorkspaceBoundary{ProjectID: "project-1"},
	)
	if err != nil {
		t.Fatalf("create filesystem context: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:              settings,
		FilesystemContext:     filesystem,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Client:                runtimePlannerClient{},
	})
	if err != nil {
		t.Fatalf("create Runtime plan: %v", err)
	}
	return plan
}

func TestRuntimePlannerAttachesReadyRuntimeWithoutPersistedReplanning(t *testing.T) {
	fixture := newRuntimePlannerFixture(t)
	sessionID := fixture.sessionID
	plan := fixture.runtimePlan(t)
	existing, err := fixture.authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "existing-owner",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open existing runtime: %v", err)
	}
	planner := NewRuntimePlanner(
		fixture.authority,
		func(context.Context, runtimeids.SessionID) (PersistedSessionPlanner, error) {
			t.Fatal("ready Runtime triggered persisted Session replanning")
			return nil, nil
		},
		fixture.runtimeAPI,
	)

	attachment, err := planner.Open(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("open ready Runtime: %v", err)
	}
	if attachment.SessionID() != sessionID {
		t.Fatalf("attachment Session = %s, want %s", attachment.SessionID(), sessionID)
	}
	if err := attachment.Release(t.Context(), sessionruntime.RuntimeReleaseDetach); err != nil {
		t.Fatalf("release Chat attachment: %v", err)
	}
	if err := fixture.authority.WithRuntime(
		t.Context(),
		existing.Resource(),
		func(context.Context, *runtime.Engine) error { return nil },
	); err != nil {
		t.Fatalf("ready Runtime was replaced or closed: %v", err)
	}
	if _, err := existing.Release(context.Background(), sessionruntime.RuntimeReleaseClose); err != nil {
		t.Fatalf("close existing Runtime: %v", err)
	}
}

type runtimePlannerSessionLaunch struct {
	request sessionlaunch.PlanRequest
	result  sessionlaunch.PlanResult
	err     error
}

func (s *runtimePlannerSessionLaunch) PlanLaunchSession(
	_ context.Context,
	request sessionlaunch.PlanRequest,
) (sessionlaunch.PlanResult, error) {
	s.request = request
	return s.result, s.err
}

type runtimePlannerRuntimeAPI struct {
	activate      serverapi.SessionRuntimeActivateRequest
	activateCalls int
	activateErr   error
	release       serverapi.SessionRuntimeReleaseRequest
	releaseErr    error
}

func (a *runtimePlannerRuntimeAPI) ActivateSessionRuntime(
	_ context.Context,
	request serverapi.SessionRuntimeActivateRequest,
) (serverapi.SessionRuntimeActivateResponse, error) {
	a.activateCalls++
	a.activate = request
	if a.activateErr != nil {
		return serverapi.SessionRuntimeActivateResponse{}, a.activateErr
	}
	return serverapi.SessionRuntimeActivateResponse{
		Attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  request.SessionID,
			Generation: 7,
		},
	}, nil
}

func (a *runtimePlannerRuntimeAPI) ReleaseSessionRuntime(
	_ context.Context,
	request serverapi.SessionRuntimeReleaseRequest,
) (serverapi.SessionRuntimeReleaseResponse, error) {
	a.release = request
	return serverapi.SessionRuntimeReleaseResponse{Released: true}, a.releaseErr
}

func TestRuntimePlannerPlansDormantPersistedSessionThenOpensIt(t *testing.T) {
	fixture := newRuntimePlannerFixture(t)
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	launchService := &runtimePlannerSessionLaunch{
		result: sessionlaunch.PlanResult{Plan: launch.SessionPlan{
			Descriptor:            mustRuntimePlannerDescriptor(t, fixture.sessionID),
			ActiveSettings:        settings,
			QuestionsEnabled:      true,
			AutoCompactionEnabled: true,
		}},
	}
	runtimeAPI := &runtimePlannerRuntimeAPI{}
	planner := NewRuntimePlanner(
		fixture.authority,
		func(_ context.Context, sessionID runtimeids.SessionID) (PersistedSessionPlanner, error) {
			if sessionID != fixture.sessionID {
				t.Fatalf("planned Session = %s, want %s", sessionID, fixture.sessionID)
			}
			return launchService, nil
		},
		runtimeAPI,
	)

	attachment, err := planner.Open(t.Context(), fixture.sessionID)
	if err != nil {
		t.Fatalf("open dormant Runtime: %v", err)
	}
	plannedSessionID, ok := launchService.request.Intent.SessionID()
	if launchService.request.Mode != launch.ModeInteractive ||
		!ok ||
		plannedSessionID != fixture.sessionID {
		t.Fatalf("persisted Session plan request = %+v", launchService.request)
	}
	if runtimeAPI.activate.SessionID != fixture.sessionID.String() ||
		runtimeAPI.activate.OwnerID == "" ||
		runtimeAPI.activate.ActiveSettings.Model != "gpt-5" {
		t.Fatalf("Runtime activation = %+v", runtimeAPI.activate)
	}
	if attachment.SessionID() != fixture.sessionID {
		t.Fatalf("attachment Session = %s, want %s", attachment.SessionID(), fixture.sessionID)
	}
	if err := attachment.Release(t.Context(), sessionruntime.RuntimeReleaseDetach); err != nil {
		t.Fatalf("release Runtime attachment: %v", err)
	}
	if runtimeAPI.release.Attachment.Generation != 7 ||
		runtimeAPI.release.OwnerID != runtimeAPI.activate.OwnerID ||
		runtimeAPI.release.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyDetachOnly {
		t.Fatalf("Runtime release = %+v", runtimeAPI.release)
	}
}

func TestRuntimePlannerServiceAttachmentRejectsUnsupportedReleasePolicy(t *testing.T) {
	fixture := newRuntimePlannerFixture(t)
	runtimeAPI := &runtimePlannerRuntimeAPI{}
	attachment := serviceRuntimeAttachment{
		sessionID: fixture.sessionID,
		attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  fixture.sessionID.String(),
			Generation: 7,
		},
		ownerID:    "chat-owner",
		runtimeAPI: runtimeAPI,
	}

	if err := attachment.Release(t.Context(), sessionruntime.RuntimeReleaseClose); err == nil {
		t.Fatal("unsupported close release policy succeeded")
	}
	if runtimeAPI.release != (serverapi.SessionRuntimeReleaseRequest{}) {
		t.Fatalf("unsupported release reached Runtime service: %+v", runtimeAPI.release)
	}
}

func TestRuntimePlannerPropagatesCancellationDuringPersistedSessionPlanning(t *testing.T) {
	fixture := newRuntimePlannerFixture(t)
	launchService := &runtimePlannerSessionLaunch{err: context.Canceled}
	runtimeAPI := &runtimePlannerRuntimeAPI{}
	planner := NewRuntimePlanner(
		fixture.authority,
		func(context.Context, runtimeids.SessionID) (PersistedSessionPlanner, error) {
			return launchService, nil
		},
		runtimeAPI,
	)

	if _, err := planner.Open(t.Context(), fixture.sessionID); err != context.Canceled {
		t.Fatalf("Open error = %v, want cancellation", err)
	}
	if runtimeAPI.activateCalls != 0 {
		t.Fatalf("canceled planning activated Runtime: %+v", runtimeAPI.activate)
	}
}

func TestRuntimePlannerPropagatesCancellationDuringRuntimeOpening(t *testing.T) {
	fixture := newRuntimePlannerFixture(t)
	launchService := &runtimePlannerSessionLaunch{
		result: sessionlaunch.PlanResult{Plan: launch.SessionPlan{
			Descriptor:     mustRuntimePlannerDescriptor(t, fixture.sessionID),
			ActiveSettings: config.DefaultOnboardingSettings(),
		}},
	}
	runtimeAPI := &runtimePlannerRuntimeAPI{activateErr: context.Canceled}
	planner := NewRuntimePlanner(
		fixture.authority,
		func(context.Context, runtimeids.SessionID) (PersistedSessionPlanner, error) {
			return launchService, nil
		},
		runtimeAPI,
	)

	if _, err := planner.Open(t.Context(), fixture.sessionID); err != context.Canceled {
		t.Fatalf("Open error = %v, want cancellation", err)
	}
}

func mustRuntimePlannerDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("create Session descriptor: %v", err)
	}
	return descriptor
}

var _ apicontract.SessionRuntimeService = (*runtimePlannerRuntimeAPI)(nil)
