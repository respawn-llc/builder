package runprompt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/runtimewire"
	"core/server/sessionlaunch"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	servicecontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/serverapi"
	"core/shared/transcriptdiag"
)

var ErrHeadlessGoalSession = errors.New("headless runs cannot continue sessions with goals; clear the goal first")

// ErrHeadlessAskUnsupported is returned by the headless ask handler when the
// model attempts to ask a question in headless/background mode, where no
// interactive answer is possible.
var ErrHeadlessAskUnsupported = errors.New("You can't ask questions in headless/background mode. If the question is critical and materially affects the task, ask it by ending your turn after trying to do as much work as possible beforehand. Otherwise, follow best practice and mention the ambiguity in your final answer.")

type promptHistoryStore interface {
	RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error)
}

type HeadlessBootstrap struct {
	SessionLaunch   *sessionlaunch.Service
	AuthManager     *auth.Manager
	FastModeState   *runtime.FastModeState
	Background      *shelltool.Manager
	RuntimeRegistry interface {
		runtimewire.RuntimeRegistry
		PublishRuntimeEvent(sessionID string, evt runtime.Event)
	}
	BackgroundRouter runtimewire.BackgroundRouter
	PromptHistory    promptHistoryStore
	// PersistenceRoot is the absolute persistence root that owns model-visible
	// global context (AGENTS.md, system prompt, skills). Empty falls back to
	// ~/.kent inside the runtime resolvers.
	PersistenceRoot string
}

func NewLoopbackRunPromptClient(boot HeadlessBootstrap) client.RunPromptClient {
	launcher := &headlessPromptLauncher{boot: boot}
	var service servicecontract.RunPromptService = NewPromptService(launcher)
	if service != nil {
		service = &memoizingPromptService{
			inner: service,
			runs:  requestmemo.New[runPromptMemoRequest, serverapi.RunPromptResponse](),
		}
	}
	return client.NewLoopbackRunPromptClient(service)
}

type headlessPromptLauncher struct {
	boot HeadlessBootstrap
}

func (l *headlessPromptLauncher) PrepareHeadlessPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (PromptSessionRuntime, error) {
	if l.boot.SessionLaunch == nil {
		return nil, errors.New("headless session launch service is required")
	}
	launchReq := serverapi.SessionPlanRequest{
		ClientRequestID:   req.ClientRequestID,
		Mode:              serverapi.SessionLaunchModeHeadless,
		SelectedSessionID: req.SelectedSessionID,
		ForceNewSession:   req.SelectedSessionID == "",
		ParentSessionID:   req.ParentSessionID,
		Overrides:         req.Overrides,
	}
	result, err := l.boot.SessionLaunch.PlanLaunchSession(ctx, launchReq)
	if err != nil {
		return nil, err
	}
	plan := result.Plan
	if plan.Store.Meta().Goal != nil {
		return nil, fmt.Errorf("%w", ErrHeadlessGoalSession)
	}
	runtimePlan, err := l.prepareRuntime(plan, progress)
	if err != nil {
		return nil, err
	}
	return &headlessPromptRuntime{plan: runtimePlan, warnings: result.Warnings, history: l.boot.PromptHistory}, nil
}

type headlessRuntimePlan struct {
	logger      *RunLogger
	engine      *runtime.Engine
	eventBridge *runtimewire.EventBridge
	close       func()
}

func (p *headlessRuntimePlan) Close() {
	if p == nil || p.close == nil {
		return
	}
	p.close()
}

func (l *headlessPromptLauncher) prepareRuntime(plan launch.SessionPlan, progress serverapi.RunPromptProgressSink) (*headlessRuntimePlan, error) {
	logger, err := NewRunLogger(plan.Store.Dir(), func(diag RunLoggerDiagnostic) {
		if progress != nil {
			progress.PublishRunPromptProgress(serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindWarning, Message: "Run logging degraded"})
		}
	})
	if err != nil {
		return nil, err
	}
	workdir := headlessRuntimeWorkdir(plan)
	logger.Logf("app.run_prompt.start session_id=%s workspace=%s workdir=%s model=%s", plan.Store.Meta().SessionID, plan.WorkspaceRoot, workdir, plan.ActiveSettings.Model)
	logger.Logf("config.settings path=%s created=%t", plan.Source.SettingsPath, plan.Source.CreatedDefaultConfig)
	for _, line := range configSourceLines(plan.Source.Sources) {
		logger.Logf("config.source %s", line)
	}
	wiring, err := runtimewire.NewRuntimeWiringWithBackground(plan.Store, plan.ActiveSettings, plan.EnabledTools, workdir, l.boot.AuthManager, logger, l.boot.Background, runtimewire.RuntimeWiringOptions{
		Headless:        true,
		FastMode:        l.boot.FastModeState,
		Sources:         plan.Source.Sources,
		GlobalConfigDir: l.boot.PersistenceRoot,
		OnEvent: func(evt runtime.Event) {
			logger.Logf("%s", FormatRuntimeEvent(evt))
			if transcriptdiag.Enabled(plan.ActiveSettings.Debug, os.Getenv) {
				projected := runtimeview.EventFromRuntime(evt)
				logger.Logf("%s", FormatTranscriptProjectionDiagnostic(plan.Store.Meta().SessionID, projected))
				logger.Logf("%s", FormatTranscriptPublishDiagnostic(plan.Store.Meta().SessionID, projected))
			}
			if l.boot.RuntimeRegistry != nil {
				l.boot.RuntimeRegistry.PublishRuntimeEvent(plan.Store.Meta().SessionID, evt)
			}
			PublishRunPromptProgress(progress, evt)
		},
	})
	if err != nil {
		_ = logger.Close()
		return nil, err
	}
	if wiring.AskBroker != nil {
		wiring.AskBroker.SetAskHandler(RunPromptAskHandler)
	}
	var runtimeRegistry runtimewire.RuntimeRegistry
	if l.boot.RuntimeRegistry != nil {
		runtimeRegistry = l.boot.RuntimeRegistry
	}
	var backgroundRouter runtimewire.BackgroundRouter
	if l.boot.BackgroundRouter != nil {
		backgroundRouter = l.boot.BackgroundRouter
	}
	var rebind func(string) error
	if wiring.LocalTools != nil {
		rebind = runtimewire.RuntimeRebindFunc(wiring.LocalTools.Rebind, wiring.Engine)
	}
	registration := runtimewire.RegisterSessionRuntime(plan.Store.Meta().SessionID, wiring.Engine, runtimeRegistry, backgroundRouter, runtimewire.WithRuntimeRebind(rebind))
	return &headlessRuntimePlan{
		logger:      logger,
		engine:      wiring.Engine,
		eventBridge: wiring.EventBridge,
		close: func() {
			_ = registration.CloseWithDrain(context.Background(), func(ctx context.Context) error {
				return wiring.Engine.DrainQueuedUserMessagesBeforeClose(ctx)
			})
			_ = wiring.Close()
			_ = logger.Close()
		},
	}, nil
}

func headlessRuntimeWorkdir(plan launch.SessionPlan) string {
	meta := plan.Store.Meta()
	if meta.WorktreeReminder != nil {
		effectiveCwd := strings.TrimSpace(meta.WorktreeReminder.EffectiveCwd)
		if effectiveCwd != "" {
			return effectiveCwd
		}
		worktreePath := strings.TrimSpace(meta.WorktreeReminder.WorktreePath)
		if worktreePath != "" {
			return worktreePath
		}
	}
	return strings.TrimSpace(plan.WorkspaceRoot)
}

type headlessPromptRuntime struct {
	plan     *headlessRuntimePlan
	warnings []string
	history  promptHistoryStore
}

func (r *headlessPromptRuntime) RecordPromptHistory(ctx context.Context, clientRequestID string, prompt string) error {
	if r == nil || r.history == nil || r.plan == nil || r.plan.engine == nil {
		return nil
	}
	requestID := strings.TrimSpace(clientRequestID)
	_, _, err := r.history.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
		SessionID: r.plan.engine.SessionID(),
		SourceID:  requestID,
		Text:      prompt,
	})
	return err
}

func (r *headlessPromptRuntime) SubmitUserMessage(ctx context.Context, prompt string) (PromptAssistantMessage, error) {
	assistant, err := r.plan.engine.SubmitUserMessage(ctx, prompt)
	return PromptAssistantMessage{
		SessionID:     r.plan.engine.SessionID(),
		SessionName:   r.plan.engine.SessionName(),
		Content:       assistant.Content,
		Warnings:      append([]string(nil), r.warnings...),
		DroppedEvents: r.plan.eventBridge.Dropped.Load(),
	}, err
}
func (r *headlessPromptRuntime) Logf(format string, args ...any) { r.plan.logger.Logf(format, args...) }
func (r *headlessPromptRuntime) Close() error {
	if r == nil || r.plan == nil {
		return nil
	}
	r.plan.Close()
	return nil
}

func RunPromptAskHandler(req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	return askquestion.AskQuestionResponse{}, ErrHeadlessAskUnsupported
}

func PublishRunPromptProgress(progress serverapi.RunPromptProgressSink, evt runtime.Event) {
	if progress == nil {
		return
	}
	state, ok := RunPromptProgressFromRuntimeEvent(evt)
	if !ok {
		return
	}
	progress.PublishRunPromptProgress(state)
}

func RunPromptProgressFromRuntimeEvent(evt runtime.Event) (serverapi.RunPromptProgress, bool) {
	switch evt.Kind {
	case runtime.EventToolCallStarted:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Running tool"}, true
	case runtime.EventToolCallCompleted:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Tool finished"}, true
	case runtime.EventReviewerCompleted:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Review finished"}, true
	case runtime.EventCompactionStarted:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Compacting context"}, true
	case runtime.EventCompactionCompleted:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Context compaction finished"}, true
	case runtime.EventCompactionFailed:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindWarning, Message: "Context compaction failed"}, true
	case runtime.EventInFlightClearFailed:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindWarning, Message: "Run cleanup warning"}, true
	default:
		return serverapi.RunPromptProgress{}, false
	}
}
