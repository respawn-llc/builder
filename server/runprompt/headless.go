package runprompt

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

var ErrHeadlessGoalSession = errors.New("headless runs cannot continue sessions with goals; clear the goal first")

var ErrSessionRunning = errors.New("selected session has an active run")

// ErrHeadlessAskUnsupported is returned by the headless ask handler when the
// model attempts to ask a question in headless/background mode, where no
// interactive answer is possible.
var ErrHeadlessAskUnsupported = errors.New("You can't ask questions in headless/background mode. If the question is critical and materially affects the task, ask it by ending your turn after trying to do as much work as possible beforehand. Otherwise, follow best practice and mention the ambiguity in your final answer.")

type promptHistoryStore interface {
	RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error)
}

type HeadlessBootstrap struct {
	SessionLaunch    *sessionlaunch.Service
	FastModeState    *runtime.FastModeState
	PromptHistory    promptHistoryStore
	RuntimeAuthority *sessionruntime.Authority
}

func NewInProcessRunPromptClient(boot HeadlessBootstrap) apicontract.RunPromptService {
	launcher := &headlessPromptLauncher{boot: boot}
	return &inProcessRunPromptService{
		launcher: launcher,
		runs:     requestmemo.New[runPromptMemoRequest, serverapi.RunPromptResponse](),
	}
}

type headlessPromptLauncher struct {
	boot HeadlessBootstrap
}

func (l *headlessPromptLauncher) prepareHeadlessPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (*headlessPromptRuntime, error) {
	if l.boot.SessionLaunch == nil {
		return nil, errors.New("headless session launch service is required")
	}
	selectedSessionID, openingExisting := req.Intent.SessionID()
	if openingExisting && l.boot.RuntimeAuthority != nil {
		if _, active := l.boot.RuntimeAuthority.SessionExecution(selectedSessionID); active {
			return nil, ErrSessionRunning
		}
	}
	launchReq := serverapi.SessionPlanRequest{
		ClientRequestID: req.ClientRequestID,
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          req.Intent,
		CallerSessionID: req.CallerSessionID,
		Overrides:       req.Overrides,
	}
	result, err := l.boot.SessionLaunch.PlanLaunchSession(ctx, launchReq)
	if err != nil {
		return nil, err
	}
	plan := result.Plan
	if plan.Goal != nil {
		return nil, fmt.Errorf("%w", ErrHeadlessGoalSession)
	}
	agentSteer, err := agentSteerForRunPrompt(req, openingExisting)
	if err != nil {
		return nil, err
	}
	runtimePlan, err := l.prepareRuntime(ctx, plan, progress, agentSteer)
	if err != nil {
		return nil, err
	}
	var sessionStarted *serverapi.RunPromptSessionStarted
	if req.Intent.Kind() == serverapi.SessionLaunchIntentCreateNew {
		sessionID, err := uuid.Parse(runtimePlan.sessionID)
		if err != nil || sessionID.Version() != 4 {
			runtimePlan.CloseWithFailure(true)
			return nil, fmt.Errorf("new headless session id %q is not a UUIDv4", runtimePlan.sessionID)
		}
		sessionStarted = &serverapi.RunPromptSessionStarted{SessionID: sessionID}
	}
	return &headlessPromptRuntime{
		plan:           runtimePlan,
		warnings:       result.Warnings,
		progress:       progress,
		sessionStarted: sessionStarted,
	}, nil
}

func agentSteerForRunPrompt(req serverapi.RunPromptRequest, openingExisting bool) (*runtime.AgentSteer, error) {
	if !openingExisting || req.CallerSessionID == nil {
		return nil, nil
	}
	sourceID, err := runtimeids.ParseSessionID(*req.CallerSessionID)
	if err != nil {
		return nil, err
	}
	steer, err := runtime.NewAgentSteer(sourceID, req.Prompt)
	if err != nil {
		return nil, err
	}
	return &steer, nil
}

type headlessRuntimePlan struct {
	handle     sessionruntime.ExecutionHandle
	sessionID  string
	submission chan headlessPromptSubmission
	content    string
	name       string
	onActive   func()
	agentSteer *runtime.AgentSteer
}

type headlessPromptSubmission struct {
	prompt string
	steer  *runtime.AgentSteer
}

func (p *headlessRuntimePlan) PromptHistoryText(fallback string) string {
	if p != nil && p.agentSteer != nil && p.agentSteer.Message().Content != nil {
		return *p.agentSteer.Message().Content
	}
	return fallback
}

func (p *headlessRuntimePlan) CloseWithFailure(failed bool) error {
	if p == nil || p.handle == nil {
		return nil
	}
	var stopErr error
	if failed {
		stopErr = p.handle.Stop(context.Background())
	}
	return errors.Join(stopErr, p.handle.Close(context.Background()))
}

func (l *headlessPromptLauncher) prepareRuntime(ctx context.Context, plan launch.SessionPlan, progress serverapi.RunPromptProgressSink, agentSteer *runtime.AgentSteer) (*headlessRuntimePlan, error) {
	if l.boot.RuntimeAuthority == nil {
		return nil, errors.New("headless run prompt requires a session runtime authority")
	}
	sessionID := plan.Descriptor.SessionID()
	executionTarget := clientui.NormalizeSessionExecutionTarget(plan.ExecutionTarget)
	workdir := executionTarget.EffectiveWorkdir
	if strings.TrimSpace(workdir) == "" {
		return nil, fmt.Errorf("headless session %q execution target effective workdir is required", sessionID)
	}
	if strings.TrimSpace(executionTarget.WorkspaceRoot) == "" {
		return nil, fmt.Errorf("headless session %q execution target workspace root is required", sessionID)
	}
	executionRoot := executionTarget.WorkspaceRoot
	var currentWorktreeRoot *string
	if executionTarget.Worktree != nil && strings.TrimSpace(executionTarget.Worktree.Root) != "" {
		root := executionTarget.Worktree.Root
		currentWorktreeRoot = &root
		executionRoot = root
	}
	filesystemContext, err := runtimewire.NewFilesystemContext(workdir, executionRoot, plan.ProjectWorkspaceBoundary)
	if err != nil {
		return nil, err
	}
	var managedWorktreePathContext *askquestion.ManagedWorktreePathContext
	if strings.TrimSpace(plan.BaseSettings.Worktrees.BaseDir) != "" {
		managedWorktreePathContext, err = askquestion.NewManagedWorktreePathContext(plan.BaseSettings.Worktrees.BaseDir, currentWorktreeRoot, plan.ManagedWorktreeRoots)
		if err != nil {
			return nil, err
		}
	}
	startLogLines := []string{
		fmt.Sprintf("app.run_prompt.start session_id=%s workspace=%s workdir=%s model=%s", sessionID, executionTarget.WorkspaceRoot, workdir, plan.ActiveSettings.Model),
		fmt.Sprintf("config.settings path=%s created=%t", plan.Source.SettingsPath, plan.Source.CreatedDefaultConfig),
	}
	for _, line := range runlog.FormatConfigSourceLines(plan.Source.Sources) {
		startLogLines = append(startLogLines, "config.source "+line)
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:          plan.ActiveSettings,
		EnabledTools:      plan.EnabledTools,
		FilesystemContext: askquestion.FilesystemContext{Access: filesystemContext.Access, ManagedWorktree: managedWorktreePathContext},
		Sources:           plan.Source.Sources,
		Headless:          true,
		FastMode:          l.boot.FastModeState,
		StartLogLines:     startLogLines,
		OnLoggingFailure: func(message string) {
			if progress != nil {
				progress.PublishRunPromptProgress(serverapi.RunPromptProgress{
					Kind:    serverapi.RunPromptProgressKindRunLoggingFailed,
					Failure: runPromptFailure(message),
				})
			}
		},
		OnEvent: func(evt runtime.Event) {
			PublishRunPromptProgress(progress, evt)
		},
	})
	if err != nil {
		return nil, err
	}
	prepared := &headlessRuntimePlan{
		sessionID:  sessionID.String(),
		submission: make(chan headlessPromptSubmission),
		agentSteer: agentSteer,
	}
	handle, err := l.boot.RuntimeAuthority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: plan.Descriptor,
		Runtime:    &runtimePlan,
		Resource:   sessionruntime.ReplaceAgentResource{},
		Ask: func(_ context.Context, _ sessionruntime.ExecutionScope, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
			return RunPromptAskHandler(req)
		},
		Runner: func(runCtx context.Context, _ sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			var submission headlessPromptSubmission
			select {
			case submission = <-prepared.submission:
			case <-runCtx.Done():
				return context.Cause(runCtx)
			}
			return bridge.WithEngine(runCtx, func(engineCtx context.Context, engine *runtime.Engine) error {
				var waitHandle *runtime.LiveRunWaitHandle
				var waitStartErr error
				submit := func() (llm.Message, error) {
					onActive := func() {
						waitHandle, waitStartErr = engine.CaptureActiveRunResult(engineCtx)
						if waitStartErr == nil && prepared.onActive != nil {
							prepared.onActive()
						}
					}
					if submission.steer != nil {
						return engine.SubmitAgentSteerWithHooks(engineCtx, *submission.steer, onActive, nil)
					}
					return engine.SubmitUserMessageWithHooks(engineCtx, submission.prompt, onActive, nil)
				}
				assistant, submitErr := submit()
				prepared.content = preservePresentAssistantContent(
					prepared.content,
					assistant,
				)
				prepared.name = engine.SessionName()
				if waitHandle != nil {
					result, waitErr := waitHandle.Wait()
					if waitErr == nil {
						prepared.content = preservePresentAssistantContent(
							prepared.content,
							result.AssistantMessage,
						)
					} else if submitErr == nil && !errors.Is(waitErr, runtime.ErrLiveRunNoFinalAnswer) {
						submitErr = waitErr
					}
				} else if waitStartErr != nil && submitErr == nil {
					submitErr = waitStartErr
				}
				return submitErr
			})
		},
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionRunActive) {
			return nil, ErrSessionRunning
		}
		return nil, err
	}
	prepared.handle = handle
	return prepared, nil
}

func preservePresentAssistantContent(current string, message llm.Message) string {
	if message.Content == nil {
		return current
	}
	return *message.Content
}

type headlessPromptRuntime struct {
	plan           *headlessRuntimePlan
	warnings       []string
	progress       serverapi.RunPromptProgressSink
	sessionStarted *serverapi.RunPromptSessionStarted
}

func (r *headlessPromptRuntime) submitUserMessage(ctx context.Context, prompt string) (serverapi.RunPromptResponse, error) {
	if r.plan == nil || r.plan.handle == nil {
		return serverapi.RunPromptResponse{}, errors.Join(serverapi.ErrRuntimeUnavailable, errors.New("headless runtime is unavailable"))
	}
	r.plan.onActive = r.publishSessionStarted
	select {
	case r.plan.submission <- headlessPromptSubmission{prompt: prompt, steer: r.plan.agentSteer}:
	case <-ctx.Done():
		return serverapi.RunPromptResponse{}, context.Cause(ctx)
	}
	_, err := r.plan.handle.Wait(ctx)
	if err != nil && context.Cause(ctx) != nil {
		err = errors.Join(err, r.plan.handle.Stop(context.Background()))
	}
	return serverapi.RunPromptResponse{
		SessionID:   r.plan.sessionID,
		SessionName: r.plan.name,
		Result:      r.plan.content,
		Warnings:    append([]string(nil), r.warnings...),
	}, err
}

func (r *headlessPromptRuntime) publishSessionStarted() {
	if r == nil || r.progress == nil || r.sessionStarted == nil {
		return
	}
	started := r.sessionStarted
	r.sessionStarted = nil
	r.progress.PublishRunPromptProgress(serverapi.RunPromptProgress{
		Kind:           serverapi.RunPromptProgressKindSessionStarted,
		SessionStarted: started,
	})
}

func RunPromptAskHandler(req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	return askquestion.AskQuestionResponse{}, ErrHeadlessAskUnsupported
}
