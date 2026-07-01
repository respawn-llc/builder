package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"core/cli/app/internal/runtimeattach"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/transcriptdiag"
)

const runtimeReleaseTimeout = runtimeattach.ReleaseTimeout

type runtimeAttachmentSource interface {
	RuntimeAttachmentClients() runtimeAttachmentClients
}

type runtimeAttachmentClients struct {
	ApprovalViews                   client.ApprovalViewClient
	AskViews                        client.AskViewClient
	Attention                       client.AttentionNotificationClient
	AttentionNotificationsSupported bool
	ProcessControls                 client.ProcessControlClient
	ProcessOutput                   client.ProcessOutputClient
	ProcessViews                    client.ProcessViewClient
	PromptActivity                  client.PromptActivityClient
	PromptControl                   client.PromptControlClient
	RuntimeControls                 client.RuntimeControlClient
	SessionActivity                 client.SessionActivityClient
	SessionRuntime                  client.SessionRuntimeClient
	SessionViews                    client.SessionViewClient
	Worktrees                       client.WorktreeClient
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
	activities, err := runtimeattach.SubscribeActivities(ctx, runtimeattach.ActivityRequest{
		SessionID:                       plan.SessionID,
		OwnerID:                         ownerID,
		Runtime:                         clients.SessionRuntime,
		SessionActivity:                 clients.SessionActivity,
		Attention:                       clients.Attention,
		AttentionNotificationsSupported: clients.AttentionNotificationsSupported,
		PromptActivity:                  clients.PromptActivity,
	})
	if err != nil {
		return nil, err
	}
	logger := &runLogger{}
	_ = diagnosticWriter
	logger.Logf("%s", startLogLine)
	wiring, stopRuntimeEvents, stopAskEvents, stopAttentionEvents := prepareSharedRuntimeWiring(ctx, clients, plan, activities, reactivator, logger)
	var stopStreamsOnce sync.Once
	stopStreams := func() {
		stopStreamsOnce.Do(func() {
			stopAttentionEvents()
			stopAskEvents()
			stopRuntimeEvents()
		})
	}
	return &runtimeLaunchPlan{
		Logger: logger,
		Wiring: wiring,
		close: func() {
			stopStreams()
			if err := runtimeattach.Release(clients.SessionRuntime, plan.SessionID, ownerID); err != nil {
				logger.Logf("runtime.release err=%q close_policy=%q session_id=%s", err.Error(), serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle, plan.SessionID)
			}
		},
		detachClose: func() {
			stopStreams()
			if err := runtimeattach.ReleaseWithClosePolicy(clients.SessionRuntime, plan.SessionID, ownerID, serverapi.SessionRuntimeReleaseClosePolicyDetachOnly); err != nil {
				logger.Logf("runtime.release err=%q close_policy=%q session_id=%s", err.Error(), serverapi.SessionRuntimeReleaseClosePolicyDetachOnly, plan.SessionID)
			}
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

func prepareSharedRuntimeWiring(ctx context.Context, clients runtimeAttachmentClients, plan sessionLaunchPlan, activities runtimeattach.Activities, reactivator *runtimeReactivator, logger *runLogger) (*runtimeWiring, func(), func(), func()) {
	runtimeClient := newUIRuntimeClientWithReads(plan.SessionID, clients.SessionViews, clients.RuntimeControls).(*sessionRuntimeClient)
	if reactivator != nil {
		runtimeClient.SetRuntimeReactivator(reactivator)
	}
	runtimeClient.SetTranscriptDiagnosticsEnabled(transcriptdiag.Enabled(plan.ActiveSettings.Debug, os.Getenv))
	runtimeEvents, stopRuntimeEvents := startSessionActivityEvents(ctx, activities.Session, func(ctx context.Context, afterSequence uint64) (serverapi.SessionActivitySubscription, error) {
		return clients.SessionActivity.SubscribeSessionActivity(ctx, serverapi.SessionActivitySubscribeRequest{SessionID: plan.SessionID, AfterSequence: afterSequence})
	}, runtimeClient.transcriptDiagnosticsEnabled, func(line string) {
		logger.Logf("%s", line)
	})
	terminalFocus := newTerminalFocusState()
	turnQueueHook := newBellHooks(newTerminalNotifier(plan.ActiveSettings.NotificationMethod, os.Stdout, os.LookupEnv), func() string {
		if runtimeClient != nil {
			if sessionName := strings.TrimSpace(runtimeClient.MainView().Session.SessionName); sessionName != "" {
				return sessionName
			}
		}
		return strings.TrimSpace(plan.SessionName)
	}, terminalFocus.FocusedForAttention)
	askEvents, stopAskEvents := newClosedAskEventStream()
	if activities.Prompt != nil {
		var promptNotificationFallback attentionNotificationHook
		if activities.Attention == nil {
			promptNotificationFallback = turnQueueHook
		}
		askEvents, stopAskEvents = startPendingPromptEvents(ctx, activities.Prompt, func(ctx context.Context, afterVersion clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
			return clients.PromptActivity.SubscribePromptActivity(ctx, serverapi.PromptActivitySubscribeRequest{SessionID: plan.SessionID, AfterReadModelVersion: afterVersion})
		}, clients.PromptControl, promptNotificationFallback)
	}
	stopAttentionEvents := startAttentionNotificationEvents(ctx, activities.Attention, func(ctx context.Context) (serverapi.AttentionNotificationSubscription, error) {
		return clients.Attention.SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: plan.SessionID, IncludePendingPromptSnapshot: true})
	}, turnQueueHook)
	wiring := &runtimeWiring{
		runtimeEvents:         runtimeEvents,
		askEvents:             askEvents,
		turnQueueHook:         turnQueueHook,
		terminalFocus:         terminalFocus,
		runtimeClient:         runtimeClient,
		promptControl:         clients.PromptControl,
		runtimeControls:       clients.RuntimeControls,
		worktrees:             clients.Worktrees,
		processControls:       clients.ProcessControls,
		processOutput:         clients.ProcessOutput,
		processViews:          clients.ProcessViews,
		approvalViews:         clients.ApprovalViews,
		askViews:              clients.AskViews,
		sessionActivity:       clients.SessionActivity,
		sessionViews:          clients.SessionViews,
		promptHistory:         append([]string(nil), plan.PromptHistory...),
		hasOtherSessions:      plan.HasOtherSessions,
		hasOtherSessionsKnown: plan.HasOtherSessionsKnown,
	}
	return wiring, stopRuntimeEvents, stopAskEvents, stopAttentionEvents
}

func newClosedAskEventStream() (<-chan askEvent, func()) {
	ch := make(chan askEvent)
	close(ch)
	return ch, func() {}
}
