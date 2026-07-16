package app

import (
	"context"
	"errors"
	"fmt"
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
	Attention                       apicontract.AttentionNotificationService
	AttentionNotificationsSupported bool
	ProcessControls                 apicontract.ProcessControlService
	ProcessOutput                   apicontract.ProcessOutputService
	ProcessViews                    apicontract.ProcessViewService
	PromptControl                   apicontract.PromptControlService
	RuntimeControls                 apicontract.RuntimeControlService
	SessionTranscript               apicontract.SessionTranscriptService
	SessionRuntime                  apicontract.SessionRuntimeService
	SessionViews                    apicontract.SessionViewService
	Worktrees                       apicontract.WorktreeService
}

func prepareSharedRuntime(ctx context.Context, source runtimeAttachmentSource, plan sessionLaunchPlan, diagnosticWriter io.Writer, startLogLine string) (*runtimeLaunchPlan, error) {
	if source == nil {
		return nil, errors.New("server is required")
	}
	clients := source.RuntimeAttachmentClients()
	reactivator, ownerID, err := activateSharedRuntime(ctx, clients, plan)
	if err != nil {
		return nil, err
	}
	if clients.SessionTranscript == nil {
		_ = runtimeattach.Release(clients.SessionRuntime, plan.SessionID, ownerID)
		return nil, errors.New("session transcript service is required")
	}
	runtimeClient := newUIRuntimeClientWithReads(plan.SessionID, clients.SessionViews, clients.RuntimeControls).(*sessionRuntimeClient)
	if reactivator != nil {
		runtimeClient.SetRuntimeReactivator(reactivator)
	}
	if _, err := runtimeClient.refreshMainViewSync(uiRuntimeHydrationReadTimeout, nil); err != nil {
		_ = runtimeattach.Release(clients.SessionRuntime, plan.SessionID, ownerID)
		return nil, fmt.Errorf("initialize runtime main view: %w", err)
	}
	var attentionSubscription serverapi.AttentionNotificationSubscription
	if clients.AttentionNotificationsSupported {
		if clients.Attention == nil {
			_ = runtimeattach.Release(clients.SessionRuntime, plan.SessionID, ownerID)
			return nil, errors.New("attention notification service is required")
		}
		attentionSubscription, err = clients.Attention.SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{
			SessionID:                    plan.SessionID,
			IncludePendingPromptSnapshot: true,
		})
		if err != nil {
			_ = runtimeattach.Release(clients.SessionRuntime, plan.SessionID, ownerID)
			return nil, err
		}
	}
	_ = diagnosticWriter
	_ = startLogLine
	wiring, stopAttentionEvents, stopTranscriptEvents := prepareSharedRuntimeWiring(
		ctx,
		clients,
		plan,
		attentionSubscription,
		runtimeClient,
	)
	var stopStreamsOnce sync.Once
	stopStreams := func() {
		stopStreamsOnce.Do(func() {
			stopAttentionEvents()
			stopTranscriptEvents()
		})
	}
	return &runtimeLaunchPlan{
		Wiring: wiring,
		close: func() error {
			stopStreams()
			return runtimeattach.Release(clients.SessionRuntime, plan.SessionID, ownerID)
		},
		detachClose: func() error {
			stopStreams()
			return runtimeattach.ReleaseWithClosePolicy(clients.SessionRuntime, plan.SessionID, ownerID, serverapi.SessionRuntimeReleaseClosePolicyDetachOnly)
		},
	}, nil
}

func activateSharedRuntime(ctx context.Context, clients runtimeAttachmentClients, plan sessionLaunchPlan) (*runtimeReactivator, string, error) {
	lease, err := runtimeattach.Activate(ctx, clients.SessionRuntime, runtimeattach.Request{
		SessionID:      plan.SessionID,
		ActiveSettings: plan.ActiveSettings,
		EnabledTools:   plan.EnabledTools,
		Source:         plan.Source,
	})
	if err != nil {
		return nil, "", err
	}
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(lease.Reactivate)
	return reactivator, lease.OwnerID, nil
}

func prepareSharedRuntimeWiring(
	ctx context.Context,
	clients runtimeAttachmentClients,
	plan sessionLaunchPlan,
	attentionSubscription serverapi.AttentionNotificationSubscription,
	runtimeClient *sessionRuntimeClient,
) (*runtimeWiring, func(), func()) {
	subscribeTranscript := func(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
		return clients.SessionTranscript.SubscribeSessionTranscript(ctx, req)
	}
	transcriptStream := startSessionTranscriptEvents(ctx, plan.SessionID, subscribeTranscript)
	transcriptEvents := transcriptStream.Events
	requestTranscriptOpen := transcriptStream.RequestRehydration
	stopTranscriptEvents := transcriptStream.Stop
	terminalFocus := newTerminalFocusState()
	turnQueueHook := newBellHooks(newTerminalNotifier(plan.ActiveSettings.NotificationMethod, os.Stdout, os.LookupEnv), func() string {
		if runtimeClient != nil {
			if sessionName := strings.TrimSpace(runtimeClient.MainView().Session.SessionName); sessionName != "" {
				return sessionName
			}
		}
		return strings.TrimSpace(plan.SessionName)
	}, terminalFocus.FocusedForAttention)
	var promptAttention *bellHooks
	if attentionSubscription == nil {
		promptAttention = turnQueueHook
	}
	stopAttentionEvents := startAttentionNotificationEvents(ctx, attentionSubscription, func(ctx context.Context) (serverapi.AttentionNotificationSubscription, error) {
		return clients.Attention.SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: plan.SessionID, IncludePendingPromptSnapshot: true})
	}, turnQueueHook)
	wiring := &runtimeWiring{
		transcriptEvents:      transcriptEvents,
		requestTranscriptOpen: requestTranscriptOpen,
		promptAnswers:         newTranscriptPromptAnswerer(ctx, clients.PromptControl),
		promptAttention:       promptAttention,
		turnQueueHook:         turnQueueHook,
		terminalFocus:         terminalFocus,
		runtimeClient:         runtimeClient,
		worktrees:             clients.Worktrees,
		processControls:       clients.ProcessControls,
		processOutput:         clients.ProcessOutput,
		processViews:          clients.ProcessViews,
		promptHistory:         append([]string(nil), plan.PromptHistory...),
	}
	return wiring, stopAttentionEvents, stopTranscriptEvents
}
