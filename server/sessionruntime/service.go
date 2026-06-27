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

	mu      sync.Mutex
	handles map[string]*runtimeHandle

	idleUnloadDelay        time.Duration
	runFinishedUnloadDelay time.Duration
	idleTimers             map[string]*runtimeIdleTimer
}

type runtimeHandle struct {
	ownerRefs      int
	ownerIDs       map[string]struct{}
	activationErr  error
	closing        bool
	ready          chan struct{}
	closed         chan struct{}
	closedOnce     sync.Once
	rebind         func(string) error
	engine         *runtime.Engine
	close          func()
	closeWithDrain func(context.Context, func(context.Context) error) error
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
		handles:                make(map[string]*runtimeHandle),
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

func (s *Service) RunOnAcquiredRuntime(ctx context.Context, sessionID string, engine *runtime.Engine, fn func(context.Context) error) error {
	if fn == nil {
		return nil
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	s.mu.Lock()
	current := s.handles[trimmedSessionID]
	overtaken := current == nil || current.engine != engine || current.closing
	var closed chan struct{}
	if !overtaken {
		closed = current.closed
	}
	s.mu.Unlock()
	if overtaken {
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
	var handle *runtimeHandle
	for {
		s.mu.Lock()
		hasLocalHandle := s.handles[sessionID] != nil
		s.mu.Unlock()
		if !hasLocalHandle {
			if _, ok := s.confirmExternalSessionRuntimeActive(ctx, sessionID); ok {
				return nil
			}
		}
		var reused, closing bool
		handle, reused, closing = s.claimActivation(sessionID, ownerID)
		if closing {
			if err := waitForRuntimeHandleClosed(ctx, handle); err != nil {
				return err
			}
			continue
		}
		if reused {
			if err := waitForRuntimeHandleReady(ctx, handle); err != nil {
				return err
			}
			s.mu.Lock()
			current := s.handles[sessionID]
			if current != handle {
				s.mu.Unlock()
				continue
			}
			if current.closing {
				s.mu.Unlock()
				if err := waitForRuntimeHandleClosed(ctx, handle); err != nil {
					return err
				}
				continue
			}
			if current.activationErr != nil {
				activationErr := current.activationErr
				s.mu.Unlock()
				return activationErr
			}
			s.addOwnerRefLocked(current, ownerID)
			s.mu.Unlock()
			s.cancelScheduledIdleUnload(sessionID)
			return nil
		}
		break
	}

	return s.buildIntoClaimedHandle(ctx, sessionID, handle, build)
}

func (s *Service) buildIntoClaimedHandle(ctx context.Context, sessionID string, handle *runtimeHandle, build RuntimeBuilder) (err error) {
	var cleanup func()
	defer func() {
		if err == nil {
			return
		}
		if cleanup != nil {
			cleanup()
		}
		s.failActivation(sessionID, handle, err)
	}()
	var built RuntimeBuildResult
	built, err = build(ctx)
	if err != nil {
		return err
	}
	var runtimeRegistry runtimewire.RuntimeRegistry
	if s.runtimes != nil {
		runtimeRegistry = s.runtimes
	}
	var backgroundRouter runtimewire.BackgroundRouter
	if s.backgroundRouter != nil {
		backgroundRouter = s.backgroundRouter
	}
	handle.rebind = runtimeRebindFunc(built.LocalRebind, built.Engine)
	registration := runtimewire.RegisterSessionRuntime(sessionID, built.Engine, runtimeRegistry, backgroundRouter, runtimewire.WithRuntimeRebind(handle.rebind))
	closeWithDrain := func(ctx context.Context, drain func(context.Context) error) error {
		err := registration.CloseWithDrain(ctx, drain)
		if built.Close != nil {
			built.Close()
		}
		return err
	}
	cleanup = func() {
		_ = closeWithDrain(context.Background(), nil)
	}
	s.completeActivation(handle, built.Engine, closeWithDrain)
	s.cancelScheduledIdleUnload(sessionID)
	cleanup = nil
	return nil
}

type AcquiredRuntimeRelease func(ctx context.Context) error

func (s *Service) RecreateRuntime(ctx context.Context, sessionID string, ownerID string, build RuntimeBuilder) (AcquiredRuntimeRelease, error) {
	sessionID = strings.TrimSpace(sessionID)
	handle, err := s.claimFreshActivation(ctx, sessionID, strings.TrimSpace(ownerID))
	if err != nil {
		return nil, err
	}
	if err := s.buildIntoClaimedHandle(ctx, sessionID, handle, build); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return s.closeAcquiredRuntime(ctx, sessionID, handle) }, nil
}

func (s *Service) claimFreshActivation(ctx context.Context, sessionID string, ownerID string) (*runtimeHandle, error) {
	for {
		s.mu.Lock()
		current := s.handles[sessionID]
		if current == nil {
			handle := newRuntimeHandle(ownerID)
			s.handles[sessionID] = handle
			s.mu.Unlock()
			return handle, nil
		}
		s.mu.Unlock()
		if err := waitForRuntimeHandleReady(ctx, current); err != nil {
			return nil, err
		}
		s.closeReleasedRuntimeHandle(sessionID, current)
	}
}

func (s *Service) RecreateRuntimeRejectingActiveRun(ctx context.Context, sessionID string, ownerID string, build RuntimeBuilder) (AcquiredRuntimeRelease, error) {
	sessionID = strings.TrimSpace(sessionID)
	handle, err := s.claimFreshActivationRejectingActiveRun(ctx, sessionID, strings.TrimSpace(ownerID))
	if err != nil {
		return nil, err
	}
	if err := s.buildIntoClaimedHandle(ctx, sessionID, handle, build); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return s.closeAcquiredRuntime(ctx, sessionID, handle) }, nil
}

func (s *Service) claimFreshActivationRejectingActiveRun(ctx context.Context, sessionID string, ownerID string) (*runtimeHandle, error) {
	for {
		s.mu.Lock()
		current := s.handles[sessionID]
		if current == nil {
			handle := newRuntimeHandle(ownerID)
			s.handles[sessionID] = handle
			s.mu.Unlock()
			return handle, nil
		}
		s.mu.Unlock()
		if err := waitForRuntimeHandleReady(ctx, current); err != nil {
			return nil, err
		}
		active, err := s.runtimeHasActiveRun(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if active {
			return nil, ErrSessionRunActive
		}
		s.closeReleasedRuntimeHandle(sessionID, current)
	}
}

func (s *Service) closeAcquiredRuntime(ctx context.Context, sessionID string, handle *runtimeHandle) error {
	if handle == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := waitForRuntimeHandleReady(ctx, handle); err != nil {
		s.closeReleasedRuntimeHandle(sessionID, handle)
		return err
	}
	s.mu.Lock()
	current := s.handles[sessionID]
	if current != handle || current.closing {
		s.mu.Unlock()
		return nil
	}
	current.closing = true
	closeWithDrain := current.closeWithDrain
	engine := current.engine
	s.mu.Unlock()
	var drainErr error
	if closeWithDrain != nil {
		drainErr = closeWithDrain(ctx, func(ctx context.Context) error {
			if engine == nil {
				return nil
			}
			return engine.DrainQueuedUserMessagesBeforeClose(ctx)
		})
	}
	s.mu.Lock()
	if s.handles[sessionID] == current {
		delete(s.handles, sessionID)
		signalRuntimeHandleClosed(current)
	}
	s.mu.Unlock()
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

func (s *Service) externalSessionRuntimeActive(sessionID string) bool {
	if s == nil || s.runtimes == nil || !s.runtimes.IsSessionRuntimeActive(sessionID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handles[strings.TrimSpace(sessionID)] == nil
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

func (s *Service) confirmExternalSessionRuntimeActive(ctx context.Context, sessionID string) (*runtime.Engine, bool) {
	if !s.externalSessionRuntimeActive(sessionID) || s.runtimes == nil {
		return nil, false
	}
	engine, err := s.runtimes.ResolveRuntime(ctx, sessionID)
	if err != nil || engine == nil {
		return nil, false
	}
	return engine, s.externalSessionRuntimeActive(sessionID)
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
	s.mu.Lock()
	handle := s.handles[sessionID]
	s.mu.Unlock()
	if handle == nil {
		return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
	}
	if err := waitForRuntimeHandleReady(ctx, handle); err != nil {
		s.closeReleasedRuntimeHandle(sessionID, handle)
		return serverapi.SessionRuntimeReleaseResponse{}, err
	}
	s.mu.Lock()
	current := s.handles[sessionID]
	if current == nil || current != handle {
		s.mu.Unlock()
		return serverapi.SessionRuntimeReleaseResponse{}, nil
	}
	if current.closing {
		s.mu.Unlock()
		return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
	}
	if trimmedOwnerID := strings.TrimSpace(req.OwnerID); trimmedOwnerID != "" {
		if _, owns := current.ownerIDs[trimmedOwnerID]; !owns {
			s.mu.Unlock()
			return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
		}
	}
	if req.OnlyIfIdle {
		if req.DropOwner && current.ownerRefs > 1 {
			current.ownerRefs--
			if trimmedOwnerID := strings.TrimSpace(req.OwnerID); trimmedOwnerID != "" {
				delete(current.ownerIDs, trimmedOwnerID)
			}
			s.mu.Unlock()
			return serverapi.SessionRuntimeReleaseResponse{}, nil
		}
		expectedOwnerRefs := current.ownerRefs
		s.mu.Unlock()
		active, err := s.runtimeHasActiveRun(ctx, sessionID)
		if err != nil {
			return serverapi.SessionRuntimeReleaseResponse{}, err
		}
		if active {
			if req.DropOwner {
				s.markRuntimeHandleOrphaned(sessionID, handle, req.OwnerID)
			}
			return serverapi.SessionRuntimeReleaseResponse{Active: true}, nil
		}
		if s.runtimeHasSubscribers(sessionID) {
			if req.DropOwner {
				s.markRuntimeHandleOrphaned(sessionID, handle, req.OwnerID)
			}
			return serverapi.SessionRuntimeReleaseResponse{}, nil
		}
		s.mu.Lock()
		current = s.handles[sessionID]
		if current == nil || current != handle || current.ownerRefs != expectedOwnerRefs {
			s.mu.Unlock()
			return serverapi.SessionRuntimeReleaseResponse{}, nil
		}
	}
	current.closing = true
	closeWithDrain := current.closeWithDrain
	closeFn := current.close
	closingEngine := current.engine
	s.mu.Unlock()
	if closeWithDrain != nil {
		_ = closeWithDrain(ctx, func(ctx context.Context) error {
			if closingEngine == nil {
				return nil
			}
			return closingEngine.DrainQueuedUserMessagesBeforeClose(ctx)
		})
	} else if closeFn != nil {
		closeFn()
	}
	s.mu.Lock()
	if s.handles[sessionID] == current {
		delete(s.handles, sessionID)
		signalRuntimeHandleClosed(current)
	}
	s.mu.Unlock()
	s.clearScheduledIdleUnload(sessionID)
	return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
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

func (s *Service) markRuntimeHandleOrphaned(sessionID string, handle *runtimeHandle, ownerID string) {
	if s == nil || handle == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	s.mu.Lock()
	current := s.handles[trimmedSessionID]
	if current == handle {
		trimmedOwnerID := strings.TrimSpace(ownerID)
		if trimmedOwnerID != "" && len(current.ownerIDs) > 0 {
			if _, ok := current.ownerIDs[trimmedOwnerID]; ok {
				delete(current.ownerIDs, trimmedOwnerID)
				if current.ownerRefs > 0 {
					current.ownerRefs--
				}
			}
		} else if current.ownerRefs > 0 {
			current.ownerRefs--
		}
	}
	s.mu.Unlock()
	s.scheduleIdleUnload(trimmedSessionID, s.defaultIdleUnloadDelay())
}

func (s *Service) addRuntimeHandleOwnerRef(sessionID string, handle *runtimeHandle, ownerID string) {
	if s == nil || handle == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	s.mu.Lock()
	if current := s.handles[trimmedSessionID]; current == handle && !current.closing {
		s.addOwnerRefLocked(current, ownerID)
	}
	s.mu.Unlock()
	s.cancelScheduledIdleUnload(trimmedSessionID)
}

func (s *Service) addOwnerRefLocked(handle *runtimeHandle, ownerID string) {
	if handle == nil {
		return
	}
	if trimmedOwnerID := strings.TrimSpace(ownerID); trimmedOwnerID != "" {
		if handle.ownerIDs == nil {
			handle.ownerIDs = make(map[string]struct{})
		}
		if _, exists := handle.ownerIDs[trimmedOwnerID]; !exists {
			handle.ownerIDs[trimmedOwnerID] = struct{}{}
			handle.ownerRefs++
		}
		return
	}
	handle.ownerRefs++
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
	handle := s.handles[trimmedSessionID]
	if handle == nil || handle.closing || handle.ownerRefs > 0 {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
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

func (s *Service) closeReleasedRuntimeHandle(sessionID string, handle *runtimeHandle) {
	if handle == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	s.mu.Lock()
	current := s.handles[trimmedSessionID]
	if current == nil || current != handle {
		s.mu.Unlock()
		return
	}
	current.closing = true
	closeFn := current.close
	s.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
	s.mu.Lock()
	if s.handles[trimmedSessionID] == current {
		delete(s.handles, trimmedSessionID)
		signalRuntimeHandleClosed(current)
	}
	s.mu.Unlock()
	s.clearScheduledIdleUnload(trimmedSessionID)
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
	handle, err := s.activeRuntimeHandle(ctx, trimmedSessionID)
	if err != nil {
		return err
	}
	if handle == nil || handle.rebind == nil {
		if s.externalSessionRuntimeActive(trimmedSessionID) {
			if err := s.WithRuntimeEngine(ctx, trimmedSessionID, func(engine *runtime.Engine) error {
				return engine.RunWhenIdle(ctx, func() error {
					engine.SetTranscriptWorkingDir(trimmedWorkdir)
					return nil
				})
			}); err != nil {
				return err
			}
		}
		return s.persistWorktreeReminderState(ctx, trimmedSessionID, normalizedReminder)
	}
	rebind := handle.rebind
	if err := s.WithRuntimeEngine(ctx, trimmedSessionID, func(engine *runtime.Engine) error {
		return engine.RunWhenIdle(ctx, func() error {
			return rebind(trimmedWorkdir)
		})
	}); err != nil {
		return err
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

func (s *Service) activeRuntimeHandle(ctx context.Context, sessionID string) (*runtimeHandle, error) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	s.mu.Lock()
	handle := s.handles[trimmedSessionID]
	s.mu.Unlock()
	if handle == nil {
		return nil, nil
	}
	if err := waitForRuntimeHandleReady(ctx, handle); err != nil {
		return nil, err
	}
	s.mu.Lock()
	current := s.handles[trimmedSessionID]
	s.mu.Unlock()
	if current == nil || current != handle {
		return nil, nil
	}
	if current.activationErr != nil {
		return nil, current.activationErr
	}
	return current, nil
}

func (s *Service) claimActivation(sessionID string, ownerID string) (handle *runtimeHandle, reused bool, closing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.handles[sessionID]; current != nil {
		if current.closing {
			return current, false, true
		}
		return current, true, false
	}
	handle = newRuntimeHandle(ownerID)
	s.handles[sessionID] = handle
	return handle, false, false
}

func newRuntimeHandle(ownerID string) *runtimeHandle {
	handle := &runtimeHandle{
		ownerRefs: 1,
		ready:     make(chan struct{}),
		closed:    make(chan struct{}),
	}
	if trimmedOwnerID := strings.TrimSpace(ownerID); trimmedOwnerID != "" {
		handle.ownerIDs = map[string]struct{}{trimmedOwnerID: {}}
	}
	return handle
}

func (s *Service) completeActivation(handle *runtimeHandle, engine *runtime.Engine, closeWithDrain func(context.Context, func(context.Context) error) error) {
	if handle == nil {
		return
	}
	handle.engine = engine
	handle.closeWithDrain = closeWithDrain
	handle.close = func() {
		if closeWithDrain != nil {
			_ = closeWithDrain(context.Background(), nil)
		}
	}
	if handle.ownerRefs <= 0 {
		handle.ownerRefs = 1
	}
	close(handle.ready)
}

func (s *Service) failActivation(sessionID string, handle *runtimeHandle, err error) {
	if handle == nil {
		return
	}
	handle.activationErr = err
	close(handle.ready)
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.handles[strings.TrimSpace(sessionID)]
	if current == nil || current != handle {
		return
	}
	delete(s.handles, strings.TrimSpace(sessionID))
}

func (s *Service) resolveExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	if s == nil || s.metadataStore == nil {
		return clientui.SessionExecutionTarget{}, fmt.Errorf("metadata store is required")
	}
	return s.metadataStore.ResolveSessionExecutionTarget(ctx, sessionID)
}

func waitForRuntimeHandleReady(ctx context.Context, handle *runtimeHandle) error {
	if handle == nil || handle.ready == nil {
		return nil
	}
	select {
	case <-handle.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitForRuntimeHandleClosed(ctx context.Context, handle *runtimeHandle) error {
	if handle == nil || handle.closed == nil {
		return nil
	}
	select {
	case <-handle.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func signalRuntimeHandleClosed(handle *runtimeHandle) {
	if handle == nil || handle.closed == nil {
		return
	}
	handle.closedOnce.Do(func() {
		close(handle.closed)
	})
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
