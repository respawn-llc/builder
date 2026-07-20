package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"core/cli/app/internal/runtimeattach"
	"core/shared/apicontract"
	"core/shared/serverapi"
)

const runtimeReleaseTimeout = runtimeattach.ReleaseTimeout

type runtimeAttachmentSource interface {
	RuntimeAttachmentClients() runtimeAttachmentClients
}

type runtimeAttachmentClients struct {
	ProcessControls   apicontract.ProcessControlService
	ProcessOutput     apicontract.ProcessOutputService
	ProcessViews      apicontract.ProcessViewService
	PromptControl     apicontract.PromptControlService
	RuntimeControls   apicontract.RuntimeControlService
	SessionTranscript apicontract.SessionTranscriptService
	SessionRuntime    apicontract.SessionRuntimeService
	SessionViews      apicontract.SessionViewService
	Worktrees         apicontract.WorktreeService
}

func prepareSharedRuntime(ctx context.Context, source runtimeAttachmentSource, plan sessionLaunchPlan, diagnosticWriter io.Writer, startLogLine string) (*runtimeLaunchPlan, error) {
	if source == nil {
		return nil, errors.New("server is required")
	}
	clients := source.RuntimeAttachmentClients()
	reactivator, lease, err := activateSharedRuntime(ctx, clients, plan)
	if err != nil {
		return nil, err
	}
	if clients.SessionTranscript == nil {
		_ = lease.Release()
		return nil, errors.New("session transcript service is required")
	}
	_ = diagnosticWriter
	_ = startLogLine
	wiring, stopTranscriptEvents := prepareSharedRuntimeWiring(
		ctx,
		clients,
		plan,
		reactivator,
	)
	var stopStreamsOnce sync.Once
	stopStreams := func() {
		stopStreamsOnce.Do(func() {
			stopTranscriptEvents()
		})
	}
	return &runtimeLaunchPlan{
		Wiring: wiring,
		close: func() error {
			stopStreams()
			return lease.Release()
		},
		detachClose: func() error {
			stopStreams()
			return lease.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyDetachOnly)
		},
	}, nil
}

func activateSharedRuntime(ctx context.Context, clients runtimeAttachmentClients, plan sessionLaunchPlan) (*runtimeReactivator, *runtimeattach.Activation, error) {
	lease, err := runtimeattach.Activate(ctx, clients.SessionRuntime, runtimeattach.Request{
		SessionID:      plan.SessionID,
		ActiveSettings: plan.ActiveSettings,
		EnabledTools:   plan.EnabledTools,
		Source:         plan.Source,
	})
	if err != nil {
		return nil, nil, err
	}
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(lease.Reactivate)
	return reactivator, lease, nil
}

func prepareSharedRuntimeWiring(
	ctx context.Context,
	clients runtimeAttachmentClients,
	plan sessionLaunchPlan,
	reactivator *runtimeReactivator,
) (*runtimeWiring, func()) {
	runtimeClient := newUIRuntimeClientWithReads(plan.SessionID, clients.SessionViews, clients.RuntimeControls).(*sessionRuntimeClient)
	if reactivator != nil {
		runtimeClient.SetRuntimeReactivator(reactivator)
	}
	subscribeTranscript := func(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
		return clients.SessionTranscript.SubscribeSessionTranscript(ctx, req)
	}
	transcriptStream := startSessionTranscriptEvents(ctx, plan.SessionID, subscribeTranscript)
	transcriptEvents := transcriptStream.Events
	requestTranscriptOpen := transcriptStream.RequestRehydration
	stopTranscriptEvents := transcriptStream.Close
	terminalFocus := newTerminalFocusState()
	turnQueueHook := newBellHooks(newTerminalNotifier(plan.ActiveSettings.NotificationMethod, os.Stdout, os.LookupEnv), func() string {
		if runtimeClient != nil {
			if sessionName := strings.TrimSpace(runtimeClient.MainView().Session.SessionName); sessionName != "" {
				return sessionName
			}
		}
		return strings.TrimSpace(plan.SessionName)
	}, terminalFocus.FocusedForAttention)
	wiring := &runtimeWiring{
		transcriptEvents:      transcriptEvents,
		requestTranscriptOpen: requestTranscriptOpen,
		promptAnswers:         newTranscriptPromptAnswerer(ctx, clients.PromptControl),
		promptAttention:       turnQueueHook,
		turnQueueHook:         turnQueueHook,
		terminalFocus:         terminalFocus,
		runtimeClient:         runtimeClient,
		worktrees:             clients.Worktrees,
		processControls:       clients.ProcessControls,
		processOutput:         clients.ProcessOutput,
		processViews:          clients.ProcessViews,
		promptHistory:         append([]string(nil), plan.PromptHistory...),
	}
	return wiring, stopTranscriptEvents
}
