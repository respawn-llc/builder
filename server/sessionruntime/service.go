package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"core/server/auth"
	"core/server/metadata"
	"core/server/registry"
	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/runtimewire"
	"core/server/session"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"
	"core/shared/transcriptdiag"

	"github.com/google/uuid"
)

type Service struct {
	persistenceRoot          string
	metadataStore            *metadata.Store
	authManager              *auth.Manager
	fastModeState            *runtime.FastModeState
	background               *shelltool.Manager
	backgroundRouter         *runtimewire.BackgroundEventRouter
	runtimes                 *registry.RuntimeRegistry
	sessionStores            *registry.SessionStoreRegistry
	storeOptions             []session.StoreOption
	recoveredWarning         string
	recoveredWarningProvider func() (string, bool, error)

	mu sync.Mutex

	idleUnloadDelay        time.Duration
	runFinishedUnloadDelay time.Duration
	idleTimers             map[string]*runtimeIdleTimer
}

type runtimeIdleTimer struct {
	generation uint64
	timer      *time.Timer
}

const (
	defaultRuntimeIdleUnloadDelay     = 5 * time.Second
	defaultRunFinishedIdleUnloadDelay = 3 * time.Minute
)

func NewService(persistenceRoot string, metadataStore *metadata.Store, authManager *auth.Manager, fastModeState *runtime.FastModeState, background *shelltool.Manager, backgroundRouter *runtimewire.BackgroundEventRouter, runtimes *registry.RuntimeRegistry, sessionStores *registry.SessionStoreRegistry, storeOptions ...session.StoreOption) *Service {
	svc := &Service{
		persistenceRoot:        strings.TrimSpace(persistenceRoot),
		metadataStore:          metadataStore,
		authManager:            authManager,
		fastModeState:          fastModeState,
		background:             background,
		backgroundRouter:       backgroundRouter,
		runtimes:               runtimes,
		sessionStores:          sessionStores,
		storeOptions:           append([]session.StoreOption(nil), storeOptions...),
		idleUnloadDelay:        defaultRuntimeIdleUnloadDelay,
		runFinishedUnloadDelay: defaultRunFinishedIdleUnloadDelay,
		idleTimers:             make(map[string]*runtimeIdleTimer),
	}
	if runtimes != nil {
		runtimes.SetInterestObserver(svc.runtimeInterestChanged)
	}
	return svc
}

func (s *Service) WithGeneratedRecoveredWarning(warning string) *Service {
	if s == nil {
		return nil
	}
	s.recoveredWarning = strings.TrimSpace(warning)
	return s
}

func (s *Service) WithGeneratedRecoveredWarningProvider(provider func() (string, bool, error)) *Service {
	if s == nil {
		return nil
	}
	s.recoveredWarningProvider = provider
	return s
}

type recoveredWarningEntry struct {
	Visibility transcript.EntryVisibility `json:"visibility,omitempty"`
	Role       string                     `json:"role"`
	Text       string                     `json:"text"`
}

func (s *Service) appendRecoveredWarningIfNeeded(store *session.Store) error {
	warning, ok, _ := s.generatedRecoveredWarning()
	if !ok || warning == "" || store == nil {
		return nil
	}
	if store.Meta().GeneratedRecoveredWarningIssued {
		return nil
	}
	_, _, appendErr := store.AppendEvent("", "local_entry", recoveredWarningEntry{
		Visibility: transcript.EntryVisibilityAll,
		Role:       "warning",
		Text:       warning,
	})
	if appendErr != nil {
		return appendErr
	}
	return store.MarkGeneratedRecoveredWarningIssued()
}

func (s *Service) generatedRecoveredWarning() (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	if s.recoveredWarningProvider != nil {
		warning, ok, err := s.recoveredWarningProvider()
		return strings.TrimSpace(warning), ok, err
	}
	warning := strings.TrimSpace(s.recoveredWarning)
	return warning, warning != "", nil
}

func (s *Service) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	ownerID := strings.TrimSpace(req.OwnerID)
	if err := s.AcquireRuntime(ctx, sessionID, ownerID, s.interactiveRuntimeBuilder(req, sessionID)); err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	return activationResponse(), nil
}

type RuntimeBuildResult struct {
	Engine      *runtime.Engine
	LocalRebind func(string) error
	Close       func()
}

var ErrSessionRunActive = errors.New("session has an active run")
var ErrAcquiredRuntimeOvertaken = errors.New("acquired runtime was overtaken or closed before the operation completed")
var ErrSessionRunsBlocked = errors.New("session runs are blocked while its worktree is being deleted")

func (s *Service) RunOnAcquiredRuntime(ctx context.Context, sessionID string, engine *runtime.Engine, fn func(context.Context) error) error {
	if fn == nil {
		return nil
	}
	if s.runtimes == nil {
		return ErrAcquiredRuntimeOvertaken
	}
	closed, ok := s.runtimes.AcquiredRuntimeClosed(strings.TrimSpace(sessionID), engine)
	if !ok {
		return ErrAcquiredRuntimeOvertaken
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-closed:
			cancel()
		case <-runCtx.Done():
		}
	}()
	err := fn(runCtx)
	select {
	case <-closed:
		return errors.Join(ErrAcquiredRuntimeOvertaken, err)
	default:
		return err
	}
}

type RuntimeBuilder func(ctx context.Context) (RuntimeBuildResult, error)

func (s *Service) AcquireRuntime(ctx context.Context, sessionID string, ownerID string, build RuntimeBuilder) error {
	sessionID = strings.TrimSpace(sessionID)
	ownerID = strings.TrimSpace(ownerID)
	if s.runtimes == nil {
		return runtimeUnavailableErr(sessionID)
	}
	for {
		claim, reused, closing := s.runtimes.AcquireRuntimeClaim(sessionID, ownerID)
		if claim == nil {
			return runtimeUnavailableErr(sessionID)
		}
		if closing {
			if err := claim.AwaitClosed(ctx); err != nil {
				return err
			}
			continue
		}
		if !reused {
			return s.buildIntoClaim(ctx, sessionID, claim, build)
		}
		if _, err := claim.AwaitReady(ctx); err != nil {
			return err
		}
		outcome, activationErr := claim.JoinAsOwner(ownerID)
		switch outcome {
		case registry.ClaimJoined:
			s.cancelScheduledIdleUnload(sessionID)
			return nil
		case registry.ClaimFailed:
			return activationErr
		case registry.ClaimClosing:
			if err := claim.AwaitClosed(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Service) buildIntoClaim(ctx context.Context, sessionID string, claim *registry.RuntimeClaim, build RuntimeBuilder) (err error) {
	var cleanup func()
	defer func() {
		if err == nil {
			return
		}
		if cleanup != nil {
			cleanup()
		}
		claim.Fail(err)
	}()
	var built RuntimeBuildResult
	built, err = build(ctx)
	if err != nil {
		return err
	}
	engine := built.Engine
	rebind := runtimeRebindFunc(built.LocalRebind, engine)
	if s.backgroundRouter != nil {
		s.backgroundRouter.SetActiveSession(sessionID, engine)
	}
	teardown := func() {
		if s.backgroundRouter != nil {
			s.backgroundRouter.ClearActiveSession(sessionID, engine)
		}
		if built.Close != nil {
			built.Close()
		}
	}
	cleanup = teardown
	claim.Resolve(engine, rebind, teardown)
	s.cancelScheduledIdleUnload(sessionID)
	cleanup = nil
	return nil
}

type AcquiredRuntimeRelease func(ctx context.Context) error

func (s *Service) RecreateRuntime(ctx context.Context, sessionID string, ownerID string, build RuntimeBuilder) (AcquiredRuntimeRelease, error) {
	return s.recreateRuntime(ctx, sessionID, ownerID, build, nil)
}

func (s *Service) RecreateRuntimeRejectingActiveRun(ctx context.Context, sessionID string, ownerID string, build RuntimeBuilder) (AcquiredRuntimeRelease, error) {
	return s.recreateRuntime(ctx, sessionID, ownerID, build, func(engine *runtime.Engine) error {
		if engine != nil && engine.ActiveRun() != nil {
			return ErrSessionRunActive
		}
		return nil
	})
}

func (s *Service) recreateRuntime(ctx context.Context, sessionID string, ownerID string, build RuntimeBuilder, beforeReplace func(*runtime.Engine) error) (AcquiredRuntimeRelease, error) {
	sessionID = strings.TrimSpace(sessionID)
	if s.runtimes == nil {
		return nil, runtimeUnavailableErr(sessionID)
	}
	releaseRun, ok := s.runtimes.BeginSessionRun(sessionID)
	if !ok {
		return nil, errors.Join(ErrSessionRunsBlocked, fmt.Errorf("session %q runs are blocked", sessionID))
	}
	defer releaseRun()
	claim, err := s.runtimes.ClaimFreshRuntime(ctx, sessionID, strings.TrimSpace(ownerID), beforeReplace)
	if err != nil {
		return nil, err
	}
	if err := s.buildIntoClaim(ctx, sessionID, claim, build); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return s.closeAcquiredClaim(ctx, sessionID, claim) }, nil
}

func (s *Service) closeAcquiredClaim(ctx context.Context, sessionID string, claim *registry.RuntimeClaim) error {
	if claim == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if _, err := claim.AwaitReady(ctx); err != nil {
		_, _ = claim.Close(ctx, nil)
		return err
	}
	engine := claim.Engine()
	_, drainErr := claim.Close(ctx, func(ctx context.Context) error {
		if engine == nil {
			return nil
		}
		return engine.DrainQueuedUserMessagesBeforeClose(ctx)
	})
	s.clearScheduledIdleUnload(sessionID)
	return drainErr
}

func (s *Service) interactiveRuntimeBuilder(req serverapi.SessionRuntimeActivateRequest, sessionID string) RuntimeBuilder {
	return func(ctx context.Context) (RuntimeBuildResult, error) {
		store, err := s.resolveStore(ctx, sessionID)
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		if err := store.EnsureDurable(); err != nil {
			return RuntimeBuildResult{}, err
		}
		if err := s.appendRecoveredWarningIfNeeded(store); err != nil {
			return RuntimeBuildResult{}, err
		}
		target, err := s.resolveExecutionTarget(ctx, sessionID)
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return RuntimeBuildResult{}, err
		}
		logger, err := runlog.NewRunLogger(store.Dir(), nil)
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		logger.Logf("app.interactive.start session_id=%s workspace=%s workdir=%s model=%s", sessionID, target.WorkspaceRoot, target.EffectiveWorkdir, req.ActiveSettings.Model)
		logger.Logf("config.settings path=%s created=%t", req.Source.SettingsPath, req.Source.CreatedDefaultConfig)
		for _, line := range configSourceLines(req.Source.Sources) {
			logger.Logf("config.source %s", line)
		}
		enabledTools, err := parseToolIDs(req.EnabledToolIDs)
		if err != nil {
			_ = logger.Close()
			return RuntimeBuildResult{}, err
		}
		wiring, err := runtimewire.NewRuntimeWiringWithBackground(store, req.ActiveSettings, enabledTools, target.EffectiveWorkdir, s.authManager, logger, s.background, runtimewire.RuntimeWiringOptions{
			FastMode:        s.fastModeState,
			Sources:         req.Source.Sources,
			GlobalConfigDir: s.persistenceRoot,
			OnEvent: func(evt runtime.Event) {
				logger.Logf("%s", runlog.FormatRuntimeEvent(evt))
				if transcriptdiag.Enabled(req.ActiveSettings.Debug, os.Getenv) {
					projected := runtimeview.EventFromRuntime(evt)
					logger.Logf("%s", runlog.FormatTranscriptProjectionDiagnostic(sessionID, projected))
					logger.Logf("%s", runlog.FormatTranscriptPublishDiagnostic(sessionID, projected))
				}
				if s.runtimes != nil {
					s.runtimes.PublishRuntimeEvent(sessionID, evt)
				}
			},
		})
		if err != nil {
			_ = logger.Close()
			return RuntimeBuildResult{}, err
		}
		if wiring.AskBroker != nil && s.runtimes != nil {
			wiring.AskBroker.SetAskHandler(func(req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
				return s.runtimes.AwaitPromptResponse(context.Background(), sessionID, req)
			})
		}
		var localRebind func(string) error
		if wiring.LocalTools != nil {
			localRebind = wiring.LocalTools.Rebind
		}
		return RuntimeBuildResult{
			Engine:      wiring.Engine,
			LocalRebind: localRebind,
			Close: func() {
				_ = wiring.Close()
				_ = logger.Close()
			},
		}, nil
	}
}

func activationResponse() serverapi.SessionRuntimeActivateResponse {
	return serverapi.SessionRuntimeActivateResponse{}
}

func (s *Service) SessionRunActive(sessionID string) bool {
	if s == nil || s.runtimes == nil {
		return false
	}
	return s.runtimes.ExternalRuntimeStatus(sessionID).State == clientui.ExternalRuntimeStateOwnerRunning
}

func (s *Service) WithRuntimeEngine(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) error {
	if s == nil || s.runtimes == nil {
		return runtimeUnavailableErr(sessionID)
	}
	id := strings.TrimSpace(sessionID)
	engine, err := s.runtimes.ResolveRuntime(ctx, id)
	if err != nil {
		return err
	}
	if engine == nil {
		return runtimeUnavailableErr(id)
	}
	return fn(engine)
}

func runtimeUnavailableErr(sessionID string) error {
	return errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("session %q has no active runtime available", strings.TrimSpace(sessionID)))
}

func runtimeRebindFunc(localRebind func(string) error, engine *runtime.Engine) func(string) error {
	return func(workdir string) error {
		if localRebind != nil {
			if err := localRebind(workdir); err != nil {
				return err
			}
		}
		if engine != nil {
			engine.SetTranscriptWorkingDir(workdir)
		}
		return nil
	}
}

func (s *Service) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if s.runtimes == nil {
		return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
	}
	claim := s.runtimes.RuntimeClaimFor(sessionID)
	if claim == nil {
		return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
	}
	if _, err := claim.AwaitReady(ctx); err != nil {
		_, _ = claim.Close(ctx, nil)
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	decision, expectedRefs := claim.BeginRelease(req.OwnerID, req.DropOwner, req.OnlyIfIdle)
	switch decision {
	case registry.RuntimeReleaseStale, registry.RuntimeReleaseDroppedRef:
		return serverapi.SessionRuntimeReleaseResponse{}, nil
	case registry.RuntimeReleaseClosing, registry.RuntimeReleaseNotOwner:
		return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
	case registry.RuntimeReleaseIdleCheck:
		active, err := s.runtimeHasActiveRun(ctx, sessionID)
		if err != nil {
			return serverapi.SessionRuntimeReleaseResponse{}, err
		}
		if active {
			if req.DropOwner {
				s.markClaimOrphaned(sessionID, claim, req.OwnerID)
			}
			return serverapi.SessionRuntimeReleaseResponse{Active: true}, nil
		}
		if s.runtimeHasSubscribers(sessionID) {
			if req.DropOwner {
				s.markClaimOrphaned(sessionID, claim, req.OwnerID)
			}
			return serverapi.SessionRuntimeReleaseResponse{}, nil
		}
		closed, err := claim.CloseIfIdle(ctx, expectedRefs, s.drainBeforeClose(claim))
		if err != nil {
			return serverapi.SessionRuntimeReleaseResponse{}, err
		}
		if !closed {
			return serverapi.SessionRuntimeReleaseResponse{}, nil
		}
		s.clearScheduledIdleUnload(sessionID)
		return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
	default:
		if _, err := claim.Close(ctx, s.drainBeforeClose(claim)); err != nil {
			return serverapi.SessionRuntimeReleaseResponse{}, err
		}
		s.clearScheduledIdleUnload(sessionID)
		return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
	}
}

func (s *Service) drainBeforeClose(claim *registry.RuntimeClaim) func(context.Context) error {
	engine := claim.Engine()
	return func(ctx context.Context) error {
		if engine == nil {
			return nil
		}
		return engine.DrainQueuedUserMessagesBeforeClose(ctx)
	}
}

func (s *Service) runtimeHasActiveRun(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.runtimes == nil {
		return false, nil
	}
	engine, err := s.runtimes.ResolveRuntime(ctx, strings.TrimSpace(sessionID))
	if err != nil || engine == nil {
		return false, err
	}
	return engine.ActiveRun() != nil, nil
}

func (s *Service) markClaimOrphaned(sessionID string, claim *registry.RuntimeClaim, ownerID string) {
	if s == nil || claim == nil {
		return
	}
	claim.DropOwner(ownerID)
	s.scheduleIdleUnload(strings.TrimSpace(sessionID), s.defaultIdleUnloadDelay())
}

func (s *Service) runtimeInterestChanged(sessionID string, reason registry.RuntimeInterestReason) {
	delay := s.defaultIdleUnloadDelay()
	if reason == registry.RuntimeInterestRunFinished {
		delay = s.runFinishedIdleUnloadDelay()
	}
	s.scheduleIdleUnload(sessionID, delay)
}

func (s *Service) cancelScheduledIdleUnload(sessionID string) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	s.mu.Lock()
	state := s.idleTimers[trimmedSessionID]
	if state != nil {
		state.generation++
		if state.timer != nil {
			state.timer.Stop()
		}
	}
	s.mu.Unlock()
}

func (s *Service) clearScheduledIdleUnload(sessionID string) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	s.mu.Lock()
	state := s.idleTimers[trimmedSessionID]
	if state != nil && state.timer != nil {
		state.timer.Stop()
	}
	delete(s.idleTimers, trimmedSessionID)
	s.mu.Unlock()
}

func (s *Service) defaultIdleUnloadDelay() time.Duration {
	if s == nil || s.idleUnloadDelay <= 0 {
		return defaultRuntimeIdleUnloadDelay
	}
	return s.idleUnloadDelay
}

func (s *Service) runFinishedIdleUnloadDelay() time.Duration {
	if s == nil || s.runFinishedUnloadDelay <= 0 {
		return defaultRunFinishedIdleUnloadDelay
	}
	return s.runFinishedUnloadDelay
}

func (s *Service) scheduleIdleUnload(sessionID string, delay time.Duration) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" || delay <= 0 {
		return
	}
	s.mu.Lock()
	if s.idleTimers == nil {
		s.idleTimers = make(map[string]*runtimeIdleTimer)
	}
	state := s.idleTimers[trimmedSessionID]
	if state == nil {
		state = &runtimeIdleTimer{}
		s.idleTimers[trimmedSessionID] = state
	}
	state.generation++
	generation := state.generation
	if state.timer != nil {
		state.timer.Stop()
	}
	state.timer = time.AfterFunc(delay, func() {
		s.runScheduledIdleUnload(trimmedSessionID, generation)
	})
	s.mu.Unlock()
}

func (s *Service) runScheduledIdleUnload(sessionID string, generation uint64) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	s.mu.Lock()
	state := s.idleTimers[trimmedSessionID]
	if state == nil || state.generation != generation {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	claim := s.runtimes.RuntimeClaimFor(trimmedSessionID)
	if claim == nil || claim.Closing() || claim.OwnerCount() > 0 {
		return
	}
	if s.runtimeHasSubscribers(trimmedSessionID) {
		return
	}
	if active, err := s.runtimeHasActiveRun(context.Background(), trimmedSessionID); err != nil || active {
		return
	}
	_, _ = s.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       trimmedSessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
	})
}

func (s *Service) runtimeHasSubscribers(sessionID string) bool {
	if s == nil || s.runtimes == nil {
		return false
	}
	return s.runtimes.HasRuntimeSubscribers(strings.TrimSpace(sessionID))
}

func (s *Service) HasActiveRun(ctx context.Context, sessionID string) (bool, error) {
	return s.runtimeHasActiveRun(ctx, sessionID)
}

func (s *Service) SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	trimmedWorkdir := strings.TrimSpace(target.EffectiveWorkdir)
	if trimmedSessionID == "" {
		return errors.New("session id is required")
	}
	if trimmedWorkdir == "" {
		return errors.New("execution target effective workdir is required")
	}
	var normalizedReminder *session.WorktreeReminderState
	if reminder != nil {
		normalized, err := normalizeWorktreeReminderState(*reminder)
		if err != nil {
			return err
		}
		normalizedReminder = &normalized
	}
	claim, err := s.activeRuntimeClaim(ctx, trimmedSessionID)
	if err != nil {
		return err
	}
	if claim != nil {
		if err := s.WithRuntimeEngine(ctx, trimmedSessionID, func(engine *runtime.Engine) error {
			return engine.RunWhenIdle(ctx, func() error {
				return claim.Rebind(trimmedWorkdir)
			})
		}); err != nil {
			return err
		}
	}
	return s.persistWorktreeReminderState(ctx, trimmedSessionID, normalizedReminder)
}

func (s *Service) persistWorktreeReminderState(ctx context.Context, sessionID string, reminder *session.WorktreeReminderState) error {
	if reminder == nil {
		return nil
	}
	store, err := s.resolveStore(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return store.SetWorktreeReminderState(reminder)
}

func normalizeWorktreeReminderState(state session.WorktreeReminderState) (session.WorktreeReminderState, error) {
	state.Mode = session.WorktreeReminderMode(strings.TrimSpace(string(state.Mode)))
	switch state.Mode {
	case session.WorktreeReminderModeEnter, session.WorktreeReminderModeExit:
	default:
		return session.WorktreeReminderState{}, errors.New("worktree reminder mode is required")
	}
	state.Branch = strings.TrimSpace(state.Branch)
	state.WorktreePath = strings.TrimSpace(state.WorktreePath)
	state.WorkspaceRoot = strings.TrimSpace(state.WorkspaceRoot)
	state.EffectiveCwd = strings.TrimSpace(state.EffectiveCwd)
	if state.WorkspaceRoot == "" {
		return session.WorktreeReminderState{}, errors.New("worktree reminder workspace root is required")
	}
	if state.EffectiveCwd == "" {
		return session.WorktreeReminderState{}, errors.New("worktree reminder effective cwd is required")
	}
	if state.Mode == session.WorktreeReminderModeEnter && state.WorktreePath == "" {
		return session.WorktreeReminderState{}, errors.New("worktree reminder worktree path is required for enter mode")
	}
	state.HasIssuedInGeneration = false
	state.IssuedCompactionCount = 0
	return state, nil
}

func (s *Service) resolveStore(ctx context.Context, sessionID string) (*session.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.sessionStores != nil {
		store, err := s.sessionStores.ResolveStore(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if store != nil {
			return store, nil
		}
	}
	store, err := session.OpenByID(s.persistenceRoot, sessionID, s.storeOptions...)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.sessionStores != nil {
		s.sessionStores.RegisterStore(store)
	}
	return store, nil
}

func (s *Service) activeRuntimeClaim(ctx context.Context, sessionID string) (*registry.RuntimeClaim, error) {
	if s.runtimes == nil {
		return nil, nil
	}
	claim := s.runtimes.RuntimeClaimFor(strings.TrimSpace(sessionID))
	if claim == nil {
		return nil, nil
	}
	if _, err := claim.AwaitReady(ctx); err != nil {
		return nil, err
	}
	if !claim.IsCurrent() {
		return nil, nil
	}
	if err := claim.ActivationErr(); err != nil {
		return nil, err
	}
	return claim, nil
}

func (s *Service) resolveExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	if s == nil || s.metadataStore == nil {
		return clientui.SessionExecutionTarget{}, fmt.Errorf("metadata store is required")
	}
	return s.metadataStore.ResolveSessionExecutionTarget(ctx, sessionID)
}

// errUnknownToolID is returned when an enabled-tool id cannot be parsed into a known tool.
var errUnknownToolID = errors.New("unknown tool id")

func parseToolIDs(raw []string) ([]toolspec.ID, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]toolspec.ID, 0, len(raw))
	for _, item := range raw {
		id, ok := toolspec.ParseID(item)
		if !ok {
			return nil, fmt.Errorf("%w %q", errUnknownToolID, item)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func configSourceLines(src map[string]string) []string {
	keys := make([]string, 0, len(src))
	for key := range src {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, strings.TrimSpace(src[key])))
	}
	return lines
}

func NewActivateRequest(clientRequestID string, sessionID string, settings config.Settings, enabledToolIDs []string, source config.SourceReport) serverapi.SessionRuntimeActivateRequest {
	id := strings.TrimSpace(clientRequestID)
	if id == "" {
		id = uuid.NewString()
	}
	return serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: id,
		SessionID:       strings.TrimSpace(sessionID),
		ActiveSettings:  settings,
		EnabledToolIDs:  append([]string(nil), enabledToolIDs...),
		Source:          source,
	}
}

var _ servicecontract.SessionRuntimeService = (*Service)(nil)
