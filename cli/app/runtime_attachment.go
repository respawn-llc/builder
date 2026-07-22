package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"core/cli/app/internal/lifecyclehook"
	"core/cli/app/internal/runtimeattach"
	"core/shared/apicontract"
	"core/shared/lifecyclecontract"
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
	wiring, stopTranscriptEvents, err := prepareSharedRuntimeWiring(
		ctx,
		clients,
		plan,
		reactivator,
	)
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	var stopStreamsOnce sync.Once
	stopStreams := func() {
		stopStreamsOnce.Do(func() {
			stopTranscriptEvents()
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
) (*runtimeWiring, func(), error) {
	runtimeClient := newUIRuntimeClientWithReads(plan.SessionID, clients.SessionViews, clients.RuntimeControls).(*sessionRuntimeClient)
	if reactivator != nil {
		runtimeClient.SetRuntimeReactivator(reactivator)
	}
	terminalFocus := newTerminalFocusState()
	initialLifecycleContext := lifecyclecontract.Context{}
	if len(plan.ClientLifecycleCommand) > 0 {
		var err error
		initialLifecycleContext, err = lifecyclehook.InitialContext(plan.SessionID, plan.SessionTitle)
		if err != nil {
			return nil, nil, err
		}
	}
	lifecycleProxy := newClientLifecycleProxy(
		ctx,
		plan.ClientLifecycleCommand,
		initialLifecycleContext,
		terminalFocus.FocusedForAttention,
	)
	subscribeTranscript := func(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
		return clients.SessionTranscript.SubscribeSessionTranscript(ctx, req)
	}
	var transcriptStream ongoingTranscriptEventStream
	if lifecycleProxy != nil {
		transcriptStream = startSessionTranscriptEvents(ctx, plan.SessionID, subscribeTranscript, lifecycleProxy.AcceptTranscript)
	} else {
		transcriptStream = startSessionTranscriptEvents(ctx, plan.SessionID, subscribeTranscript)
	}
	transcriptEvents := transcriptStream.Events
	eventDispatcher := newUIEventDispatcher(transcriptEvents)
	requestTranscriptOpen := transcriptStream.RequestRehydration
	turnQueueHook := newBellHooks(newTerminalNotifier(plan.ActiveSettings.NotificationMethod, os.Stdout, os.LookupEnv), func() string {
		if runtimeClient != nil {
			if sessionName := strings.TrimSpace(runtimeClient.MainView().Session.SessionName); sessionName != "" {
				return sessionName
			}
		}
		if plan.SessionTitle == nil {
			return ""
		}
		return strings.TrimSpace(*plan.SessionTitle)
	}, terminalFocus.FocusedForAttention)
	wiring := &runtimeWiring{
		eventDispatcher:       eventDispatcher,
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
	if lifecycleProxy != nil {
		wiring.lifecycleHookIssues = lifecycleProxy.Issues()
		wiring.lifecycleHookDone = lifecycleProxy.Done()
		lifecycleProxy.AcceptSessionStart(plan.ClientLifecycleOpeningKind)
	}
	return wiring, func() {
		transcriptStream.Stop()
		if lifecycleProxy != nil {
			lifecycleProxy.Close()
		}
	}, nil
}
