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
	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/runtimewire"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	servicecontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/serverapi"
	"core/shared/transcriptdiag"

	"github.com/google/uuid"
)

var ErrHeadlessGoalSession = errors.New("headless runs cannot continue sessions with goals; clear the goal first")

var ErrSessionRunning = errors.New("the target session is not finished - it still has an active run. Communicate with the agent via --steer or stdin on the active `run` process, stop the run, or wait for it to finish first")

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
	SessionRuntime   *sessionruntime.Service
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
	if selected := strings.TrimSpace(req.SelectedSessionID); selected != "" && l.boot.SessionRuntime != nil && l.boot.SessionRuntime.SessionRunActive(selected) {
		return nil, ErrSessionRunning
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
	runtimePlan, err := l.prepareRuntime(ctx, plan, progress)
	if err != nil {
		return nil, err
	}
	return &headlessPromptRuntime{plan: runtimePlan, warnings: result.Warnings, history: l.boot.PromptHistory}, nil
}

type headlessRuntimePlan struct {
	sessionRuntime *sessionruntime.Service
	sessionID      string
	ownerID        string
	diagLogger     *runlog.RunLogger
	eventBridge    *runtimewire.EventBridge
	close          func()
}

func (p *headlessRuntimePlan) Close() {
	if p == nil || p.close == nil {
		return
	}
	p.close()
}

func (l *headlessPromptLauncher) prepareRuntime(ctx context.Context, plan launch.SessionPlan, progress serverapi.RunPromptProgressSink) (*headlessRuntimePlan, error) {
	if l.boot.SessionRuntime == nil {
		return nil, errors.New("headless run prompt requires a session runtime service")
	}
	sessionID := plan.Store.Meta().SessionID
	ownerID := uuid.NewString()
	diagLogger, err := runlog.NewRunLogger(plan.Store.Dir(), func(diag runlog.RunLoggerDiagnostic) {
		if progress != nil {
			progress.PublishRunPromptProgress(serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindWarning, Message: "Run logging degraded"})
		}
	})
	if err != nil {
		return nil, err
	}
	workdir := headlessRuntimeWorkdir(plan)
	diagLogger.Logf("app.run_prompt.start session_id=%s workspace=%s workdir=%s model=%s", sessionID, plan.WorkspaceRoot, workdir, plan.ActiveSettings.Model)
	diagLogger.Logf("config.settings path=%s created=%t", plan.Source.SettingsPath, plan.Source.CreatedDefaultConfig)
	for _, line := range configSourceLines(plan.Source.Sources) {
		diagLogger.Logf("config.source %s", line)
	}
	var eventBridge *runtimewire.EventBridge
	build := func(ctx context.Context) (sessionruntime.RuntimeBuildResult, error) {
		engineLogger, err := runlog.NewRunLogger(plan.Store.Dir(), nil)
		if err != nil {
			return sessionruntime.RuntimeBuildResult{}, err
		}
		wiring, err := runtimewire.NewRuntimeWiringWithBackground(plan.Store, plan.ActiveSettings, plan.EnabledTools, workdir, l.boot.AuthManager, engineLogger, l.boot.Background, runtimewire.RuntimeWiringOptions{
			Headless:        true,
			FastMode:        l.boot.FastModeState,
			Sources:         plan.Source.Sources,
			GlobalConfigDir: l.boot.PersistenceRoot,
			OnEvent: func(evt runtime.Event) {
				engineLogger.Logf("%s", runlog.FormatRuntimeEvent(evt))
				if transcriptdiag.Enabled(plan.ActiveSettings.Debug, os.Getenv) {
					projected := runtimeview.EventFromRuntime(evt)
					engineLogger.Logf("%s", runlog.FormatTranscriptProjectionDiagnostic(sessionID, projected))
					engineLogger.Logf("%s", runlog.FormatTranscriptPublishDiagnostic(sessionID, projected))
				}
				if l.boot.RuntimeRegistry != nil {
					l.boot.RuntimeRegistry.PublishRuntimeEvent(sessionID, evt)
				}
				PublishRunPromptProgress(progress, evt)
			},
		})
		if err != nil {
			_ = engineLogger.Close()
			return sessionruntime.RuntimeBuildResult{}, err
		}
		if wiring.AskBroker != nil {
			wiring.AskBroker.SetAskHandler(RunPromptAskHandler)
		}
		eventBridge = wiring.EventBridge
		var localRebind func(string) error
		if wiring.LocalTools != nil {
			localRebind = wiring.LocalTools.Rebind
		}
		return sessionruntime.RuntimeBuildResult{
			Engine:      wiring.Engine,
			LocalRebind: localRebind,
			Close: func() {
				_ = wiring.Close()
				_ = engineLogger.Close()
			},
		}, nil
	}
	releaseRuntime, err := l.boot.SessionRuntime.RecreateRuntimeRejectingActiveRun(ctx, sessionID, ownerID, build)
	if err != nil {
		_ = diagLogger.Close()
		if errors.Is(err, sessionruntime.ErrSessionRunActive) {
			return nil, ErrSessionRunning
		}
		return nil, err
	}
	return &headlessRuntimePlan{
		sessionRuntime: l.boot.SessionRuntime,
		sessionID:      sessionID,
		ownerID:        ownerID,
		diagLogger:     diagLogger,
		eventBridge:    eventBridge,
		close: func() {
			_ = releaseRuntime(context.Background())
			_ = diagLogger.Close()
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
	if r == nil || r.history == nil || r.plan == nil {
		return nil
	}
	requestID := strings.TrimSpace(clientRequestID)
	_, _, err := r.history.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
		SessionID: r.plan.sessionID,
		SourceID:  requestID,
		Text:      prompt,
	})
	return err
}

func (r *headlessPromptRuntime) SubmitUserMessage(ctx context.Context, prompt string) (PromptAssistantMessage, error) {
	var content string
	var sessionName string
	err := r.plan.sessionRuntime.WithRuntimeEngine(ctx, r.plan.sessionID, func(engine *runtime.Engine) error {
		assistant, submitErr := engine.SubmitUserMessage(ctx, prompt)
		content = assistant.Content
		sessionName = engine.SessionName()
		return submitErr
	})
	var dropped uint64
	if r.plan.eventBridge != nil {
		dropped = r.plan.eventBridge.Dropped.Load()
	}
	return PromptAssistantMessage{
		SessionID:     r.plan.sessionID,
		SessionName:   sessionName,
		Content:       content,
		Warnings:      append([]string(nil), r.warnings...),
		DroppedEvents: dropped,
	}, err
}
func (r *headlessPromptRuntime) Logf(format string, args ...any) {
	r.plan.diagLogger.Logf(format, args...)
}
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
