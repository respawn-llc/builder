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
	AttentionNotifications apicontract.AttentionNotificationService
	ProcessControls        apicontract.ProcessControlService
	ProcessOutput          apicontract.ProcessOutputService
	ProcessViews           apicontract.ProcessViewService
	PromptControl          apicontract.PromptControlService
	RuntimeControls        apicontract.RuntimeControlService
	SessionTranscript      apicontract.SessionTranscriptService
	SessionRuntime         apicontract.SessionRuntimeService
	SessionViews           apicontract.SessionViewService
	Worktrees              apicontract.WorktreeService
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
	wiring, stopEventStreams := prepareSharedRuntimeWiring(
		ctx,
		clients,
		plan,
		reactivator,
	)
	var stopStreamsOnce sync.Once
	stopStreams := func() {
		stopStreamsOnce.Do(func() {
			stopEventStreams()
		})
	}
	return &runtimeLaunchPlan{
		Wiring:           wiring,
		stopEventStreams: stopStreams,
		close: func() error {
			return lease.Release()
		},
		detachClose: func() error {
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
	eventDispatcher := newUIEventDispatcher(transcriptEvents)
	requestTranscriptOpen := transcriptStream.RequestRehydration
	var attentionStream *attentionEventStream
	if clients.AttentionNotifications != nil {
		subscribeAttention := func(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
			return clients.AttentionNotifications.SubscribeSessionAttentionNotifications(ctx, req)
		}
		attentionStream = startAttentionEventStream(ctx, plan.SessionID, subscribeAttention)
	}
	stopEventStreams := func() {
		transcriptStream.Close()
		if attentionStream != nil {
			attentionStream.Close()
		}
	}
	terminalFocus := newTerminalFocusState()
	nativeTurnNotifications := newNativeTurnNotificationObserver(newTerminalNotifier(plan.ActiveSettings.NotificationMethod, os.Stdout, os.LookupEnv), func() string {
		if runtimeClient != nil {
			if sessionName := strings.TrimSpace(runtimeClient.MainView().Session.SessionName); sessionName != "" {
				return sessionName
			}
		}
		return strings.TrimSpace(plan.SessionName)
	}, terminalFocus.FocusedForAttention)
	promptAttention := nativeTurnNotifications
	if attentionStream != nil {
		promptAttention = nil
	}
	wiring := &runtimeWiring{
		eventDispatcher:         eventDispatcher,
		requestTranscriptOpen:   requestTranscriptOpen,
		promptAnswers:           newTranscriptPromptAnswerer(ctx, clients.PromptControl),
		promptAttention:         promptAttention,
		nativeTurnNotifications: nativeTurnNotifications,
		terminalFocus:           terminalFocus,
		runtimeClient:           runtimeClient,
		worktrees:               clients.Worktrees,
		processControls:         clients.ProcessControls,
		processOutput:           clients.ProcessOutput,
		processViews:            clients.ProcessViews,
		promptHistory:           append([]string(nil), plan.PromptHistory...),
	}
	if attentionStream != nil {
		eventDispatcher.attentionEvents = attentionStream.events
		eventDispatcher.requestAttentionReopen = attentionStream.RequestReopen
	}
	return wiring, stopEventStreams
}
