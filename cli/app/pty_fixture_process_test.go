package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"syscall"
	"time"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	creackpty "github.com/creack/pty"
)

type ptyCheckpointTerminalFile struct {
	file   *os.File
	writer *checkpoint.Writer
}

func runConfiguredPTYClientProcess(ctx context.Context, processConfig appfixture.ConfiguredClientProcessConfig) (runErr error) {
	if err := processConfig.Validate(); err != nil {
		return err
	}
	endpoint, err := url.Parse(processConfig.ConfiguredServerEndpoint)
	if err != nil {
		return fmt.Errorf("parse configured client endpoint: %w", err)
	}
	host, port, err := net.SplitHostPort(endpoint.Host)
	if err != nil {
		return fmt.Errorf("split configured client endpoint: %w", err)
	}
	if err := os.Setenv(config.PersistenceRootEnvName, processConfig.PersistenceRoot); err != nil {
		return err
	}
	if err := os.Setenv("KENT_SERVER_HOST", host); err != nil {
		return err
	}
	if err := os.Setenv("KENT_SERVER_PORT", port); err != nil {
		return err
	}
	originalStdout := os.Stdout
	writer := checkpoint.NewWriter(originalStdout)
	if err := writer.Emit(checkpoint.KindAppRunStarted, nil); err != nil {
		return err
	}
	previousSessionPicker := runSessionPickerFlow
	previousAuthPicker := runStartupPickerFlow
	lifecycleCtx, cancelLifecycle := context.WithCancel(ctx)
	defer cancelLifecycle()
	connectionReady := make(chan *interactiveConnectionOwner, 1)
	terminalReady := make(chan *configuredPTYTerminalProxy, 1)
	lifecycleDone := make(chan error, 1)
	go func() {
		lifecycleDone <- runConfiguredPTYMainLifecycle(
			lifecycleCtx,
			processConfig.ConfiguredServerEndpoint,
			writer,
			connectionReady,
			terminalReady,
		)
	}()
	var connectionOnce sync.Once
	var terminal *configuredPTYTerminalProxy
	runSessionPickerFlow = func(loader sessionPageLoader, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
		if header.updateStatus == nil {
			return nil, errors.New("configured PTY picker requires server update-status client")
		}
		connectionOnce.Do(func() {
			connectionReady <- header.connection
		})
		ready := &configuredPTYPickerReadyGate{writer: writer}
		header.updateStatus = &configuredPTYUpdateStatusClient{
			inner:    header.updateStatus,
			endpoint: processConfig.ConfiguredServerEndpoint,
			writer:   writer,
			ready:    ready,
		}
		result, err := previousSessionPicker(
			&configuredPTYSessionPageLoader{inner: loader, ready: ready},
			theme,
			header,
		)
		if err == nil {
			switch result.(type) {
			case sessionPickerCreateResult, sessionPickerOpenResult:
				terminal, err = newConfiguredPTYTerminalProxy(originalStdout, writer)
				if err != nil {
					return nil, err
				}
				terminal.ArmMainOutput()
				terminalReady <- terminal
			}
		}
		return result, err
	}
	runStartupPickerFlow = func(model *startupPickerModel) (startupPickerResult, error) {
		if err := writer.Emit(checkpoint.KindAuthPickerReady, nil); err != nil {
			return startupPickerResult{}, err
		}
		return previousAuthPicker(model)
	}
	defer func() {
		runSessionPickerFlow = previousSessionPicker
		runStartupPickerFlow = previousAuthPicker
		cancelLifecycle()
		if err := <-lifecycleDone; err != nil && !errors.Is(err, context.Canceled) {
			runErr = errors.Join(runErr, err)
		}
		if terminal != nil {
			if err := terminal.Close(); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
		if err := writer.Emit(checkpoint.KindAppRunExited, nil); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	return Run(ctx, Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		ConfigRoot:            processConfig.PersistenceRoot,
	})
}

type configuredPTYUpdateStatusClient struct {
	inner    apicontract.ServerStatusService
	endpoint string
	writer   *checkpoint.Writer
	ready    *configuredPTYPickerReadyGate
}

func (client *configuredPTYUpdateStatusClient) GetServerReadiness(ctx context.Context, request serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	return client.inner.GetServerReadiness(ctx, request)
}

func (client *configuredPTYUpdateStatusClient) GetUpdateStatus(ctx context.Context, request serverapi.UpdateStatusRequest) (serverapi.UpdateStatusResponse, error) {
	type result struct {
		response serverapi.UpdateStatusResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := client.inner.GetUpdateStatus(ctx, request)
		done <- result{response: response, err: err}
	}()
	if err := waitConfiguredPTYRouteEvent(ctx, client.endpoint, configuredPTYRouteEventUpdateRequestAdmitted); err != nil {
		return serverapi.UpdateStatusResponse{}, err
	}
	if err := client.ready.MarkUpdateAdmitted(); err != nil {
		return serverapi.UpdateStatusResponse{}, err
	}
	completed := <-done
	if errors.Is(completed.err, context.Canceled) {
		if err := client.writer.Emit(checkpoint.KindPickerRequestCanceled, nil); err != nil {
			return serverapi.UpdateStatusResponse{}, err
		}
	}
	return completed.response, completed.err
}

type configuredPTYPickerReadyGate struct {
	mu             sync.Mutex
	writer         *checkpoint.Writer
	updateAdmitted bool
	pageLoaded     bool
	emitted        bool
}

func (gate *configuredPTYPickerReadyGate) MarkUpdateAdmitted() error {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.updateAdmitted = true
	return gate.emitIfReady()
}

func (gate *configuredPTYPickerReadyGate) MarkPageLoaded() error {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.pageLoaded = true
	return gate.emitIfReady()
}

func (gate *configuredPTYPickerReadyGate) emitIfReady() error {
	if gate.emitted || !gate.updateAdmitted || !gate.pageLoaded {
		return nil
	}
	if err := gate.writer.Emit(checkpoint.KindSessionPickerReady, nil); err != nil {
		return err
	}
	gate.emitted = true
	return nil
}

type configuredPTYSessionPageLoader struct {
	inner sessionPageLoader
	ready *configuredPTYPickerReadyGate
}

func (loader *configuredPTYSessionPageLoader) ProjectID() string {
	return loader.inner.ProjectID()
}

func (loader *configuredPTYSessionPageLoader) ListSessionPage(ctx context.Context, request serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	response, err := loader.inner.ListSessionPage(ctx, request)
	if err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	if request.Category == sessioncontract.SessionCategoryMain && len(response.Sessions) > 0 {
		if err := loader.ready.MarkPageLoaded(); err != nil {
			return serverapi.SessionPageResponse{}, err
		}
	}
	return response, nil
}

func runConfiguredPTYMainLifecycle(
	ctx context.Context,
	endpoint string,
	writer *checkpoint.Writer,
	connectionReady <-chan *interactiveConnectionOwner,
	terminalReady <-chan *configuredPTYTerminalProxy,
) error {
	var connection *interactiveConnectionOwner
	select {
	case <-ctx.Done():
		return ctx.Err()
	case connection = <-connectionReady:
	}
	if connection == nil {
		return errors.New("configured PTY main lifecycle requires shared connection owner")
	}
	if err := waitConfiguredPTYRouteEvent(ctx, endpoint, configuredPTYRouteEventTranscriptSubscriptionEstablished); err != nil {
		return err
	}
	if err := writer.Emit(checkpoint.KindTranscriptSubscriptionEstablished, nil); err != nil {
		return err
	}
	if err := waitConfiguredPTYRouteEvent(ctx, endpoint, configuredPTYRouteEventInitialInputLoaded); err != nil {
		return err
	}
	if err := writer.Emit(checkpoint.KindInitialInputLoaded, nil); err != nil {
		return err
	}
	var terminal *configuredPTYTerminalProxy
	select {
	case <-ctx.Done():
		return ctx.Err()
	case terminal = <-terminalReady:
	}
	if terminal == nil {
		return errors.New("configured PTY main lifecycle requires terminal proxy")
	}
	if err := terminal.WaitMainOutput(ctx); err != nil {
		return err
	}
	transportLossFrameGeneration, connectedStatusRow := terminal.NativeFrameState()
	disconnectedFrameRelease := make(chan struct{})
	disconnectedFrame, err := terminal.CheckpointAfterNativeFrame(
		ctx,
		checkpoint.KindMainUIReady,
		transportLossFrameGeneration,
		func(statusRow []checkpoint.Cell) bool {
			return connection.IsDisconnected() &&
				!configuredPTYStatusRowsEqual(statusRow, connectedStatusRow)
		},
		disconnectedFrameRelease,
	)
	if err != nil {
		return err
	}
	if err := failConfiguredPTYTranscript(ctx, endpoint); err != nil {
		return err
	}
	if err := waitConfiguredPTYRouteEvent(ctx, endpoint, configuredPTYRouteEventTranscriptTransportLost); err != nil {
		return err
	}
	if err := writer.Emit(checkpoint.KindTranscriptTransportLost, nil); err != nil {
		return err
	}
	close(disconnectedFrameRelease)
	reachableFrameRelease := make(chan struct{})
	var reachableFrame <-chan configuredPTYNativeFrameCheckpointResult
	if err := waitConfiguredPTYNativeFrame(ctx, disconnectedFrame, func() error {
		recoveryFrameGeneration, _ := terminal.NativeFrameState()
		var checkpointErr error
		reachableFrame, checkpointErr = terminal.CheckpointAfterNativeFrame(
			ctx,
			checkpoint.KindMainRequestReachable,
			recoveryFrameGeneration,
			func(statusRow []checkpoint.Cell) bool {
				return !connection.IsDisconnected() &&
					configuredPTYStatusRowsEqual(statusRow, connectedStatusRow)
			},
			reachableFrameRelease,
		)
		return checkpointErr
	}); err != nil {
		return err
	}
	if err := resumeConfiguredPTYTranscript(ctx, endpoint); err != nil {
		return err
	}
	if err := waitConfiguredPTYRouteEvent(ctx, endpoint, configuredPTYRouteEventMainTranscriptResubscribed); err != nil {
		return err
	}
	close(reachableFrameRelease)
	if err := waitConfiguredPTYNativeFrame(ctx, reachableFrame, nil); err != nil {
		return err
	}
	return nil
}

func configuredPTYStatusRowsEqual(left, right []checkpoint.Cell) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Content != right[index].Content {
			return false
		}
	}
	return true
}

type configuredPTYTerminalProxy struct {
	original  *os.File
	master    *os.File
	slave     *os.File
	writer    *checkpoint.Writer
	readDone  chan error
	readiness *checkpoint.ReadinessTracker

	mu                    sync.Mutex
	mainOutputArmed       bool
	mainOutput            chan struct{}
	nativeFrameGeneration uint64
	nativeFrameStatusRow  []checkpoint.Cell
	readinessChunk        int
	pending               []configuredPTYAfterFrameCheckpoint
	closed                bool
}

type configuredPTYAfterFrameCheckpoint struct {
	kind                       checkpoint.Kind
	ctx                        context.Context
	afterNativeFrameGeneration uint64
	predicate                  func([]checkpoint.Cell) bool
	release                    <-chan struct{}
	done                       chan configuredPTYNativeFrameCheckpointResult
}

type configuredPTYNativeFrameCheckpointResult struct {
	err         error
	acknowledge chan struct{}
}

func newConfiguredPTYTerminalProxy(original *os.File, writer *checkpoint.Writer) (*configuredPTYTerminalProxy, error) {
	if original == nil {
		return nil, errors.New("configured PTY terminal requires stdout")
	}
	if writer == nil {
		return nil, errors.New("configured PTY terminal requires checkpoint writer")
	}
	size, err := creackpty.GetsizeFull(original)
	if err != nil {
		return nil, fmt.Errorf("read configured PTY terminal size: %w", err)
	}
	master, slave, err := creackpty.Open()
	if err != nil {
		return nil, fmt.Errorf("open configured PTY forwarding terminal: %w", err)
	}
	if err := creackpty.Setsize(master, size); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("size configured PTY forwarding terminal: %w", err)
	}
	dimensions, err := checkpoint.NewDimensions(int(size.Rows), int(size.Cols))
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("create configured PTY dimensions: %w", err)
	}
	readiness, err := checkpoint.NewReadinessTracker(dimensions)
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("create configured PTY readiness tracker: %w", err)
	}
	proxy := &configuredPTYTerminalProxy{
		original:   original,
		master:     master,
		slave:      slave,
		writer:     writer,
		readDone:   make(chan error, 1),
		readiness:  readiness,
		mainOutput: make(chan struct{}),
	}
	os.Stdout = slave
	go proxy.forward()
	return proxy, nil
}

func (proxy *configuredPTYTerminalProxy) forward() {
	buffer := make([]byte, 32*1024)
	for {
		n, err := proxy.master.Read(buffer)
		if n > 0 {
			if writeErr := proxy.forwardPayload(buffer[:n]); writeErr != nil {
				proxy.readDone <- writeErr
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) {
				proxy.readDone <- nil
			} else {
				proxy.readDone <- err
			}
			return
		}
	}
}

func (proxy *configuredPTYTerminalProxy) forwardPayload(payload []byte) error {
	segmentStart := 0
	for index := range payload {
		before := proxy.readiness.ByteCount()
		chunk := checkpoint.NewChunk(
			proxy.readinessChunk,
			time.Duration(proxy.readinessChunk+1),
			payload[index:index+1],
		)
		proxy.readinessChunk++
		if err := proxy.readiness.AdvanceChunk(chunk); err != nil {
			return err
		}
		if _, completed := proxy.readiness.LatestBoundaryAfter(
			checkpoint.ReadinessNativeOngoingFrame,
			before,
		); !completed {
			continue
		}
		if _, err := proxy.writer.Write(payload[segmentStart : index+1]); err != nil {
			return err
		}
		screen := proxy.readiness.ScreenSnapshot()
		statusRow := append([]checkpoint.Cell(nil), screen.Cells[screen.Dimensions.Rows-1]...)
		proxy.afterNativeFrame(statusRow)
		segmentStart = index + 1
	}
	if segmentStart < len(payload) {
		if _, err := proxy.writer.Write(payload[segmentStart:]); err != nil {
			return err
		}
	}
	return nil
}

func (proxy *configuredPTYTerminalProxy) afterNativeFrame(statusRow []checkpoint.Cell) {
	proxy.mu.Lock()
	proxy.nativeFrameGeneration++
	proxy.nativeFrameStatusRow = append(proxy.nativeFrameStatusRow[:0], statusRow...)
	if proxy.mainOutputArmed {
		proxy.mainOutputArmed = false
		select {
		case <-proxy.mainOutput:
		default:
			close(proxy.mainOutput)
		}
	}
	ready := make([]configuredPTYAfterFrameCheckpoint, 0, len(proxy.pending))
	pending := proxy.pending[:0]
	for _, item := range proxy.pending {
		if proxy.nativeFrameGeneration > item.afterNativeFrameGeneration &&
			item.predicate(proxy.nativeFrameStatusRow) {
			ready = append(ready, item)
		} else {
			pending = append(pending, item)
		}
	}
	proxy.pending = pending
	proxy.mu.Unlock()
	for _, item := range ready {
		select {
		case <-item.release:
		case <-item.ctx.Done():
			item.done <- configuredPTYNativeFrameCheckpointResult{err: item.ctx.Err()}
			close(item.done)
			continue
		}
		err := proxy.writer.Emit(item.kind, nil)
		acknowledge := make(chan struct{})
		item.done <- configuredPTYNativeFrameCheckpointResult{
			err:         err,
			acknowledge: acknowledge,
		}
		select {
		case <-acknowledge:
		case <-item.ctx.Done():
		}
		close(item.done)
	}
}

func (proxy *configuredPTYTerminalProxy) ArmMainOutput() {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.mainOutputArmed = true
}

func (proxy *configuredPTYTerminalProxy) WaitMainOutput(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-proxy.mainOutput:
		return nil
	}
}

func (proxy *configuredPTYTerminalProxy) NativeFrameState() (uint64, []checkpoint.Cell) {
	if proxy == nil {
		return 0, nil
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return proxy.nativeFrameGeneration, append([]checkpoint.Cell(nil), proxy.nativeFrameStatusRow...)
}

func (proxy *configuredPTYTerminalProxy) CheckpointAfterNativeFrame(
	ctx context.Context,
	kind checkpoint.Kind,
	afterNativeFrameGeneration uint64,
	predicate func([]checkpoint.Cell) bool,
	release <-chan struct{},
) (<-chan configuredPTYNativeFrameCheckpointResult, error) {
	if ctx == nil {
		return nil, errors.New("configured PTY native-frame checkpoint context is required")
	}
	if predicate == nil {
		return nil, errors.New("configured PTY native-frame predicate is required")
	}
	if release == nil {
		return nil, errors.New("configured PTY native-frame checkpoint release is required")
	}
	done := make(chan configuredPTYNativeFrameCheckpointResult, 1)
	proxy.mu.Lock()
	if proxy.closed {
		proxy.mu.Unlock()
		return nil, io.ErrClosedPipe
	}
	proxy.pending = append(proxy.pending, configuredPTYAfterFrameCheckpoint{
		kind:                       kind,
		ctx:                        ctx,
		afterNativeFrameGeneration: afterNativeFrameGeneration,
		predicate:                  predicate,
		release:                    release,
		done:                       done,
	})
	proxy.mu.Unlock()
	return done, nil
}

func waitConfiguredPTYNativeFrame(
	ctx context.Context,
	done <-chan configuredPTYNativeFrameCheckpointResult,
	beforeAcknowledge func() error,
) error {
	if done == nil {
		return errors.New("configured PTY native-frame observation is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result, ok := <-done:
		if !ok {
			return io.ErrClosedPipe
		}
		if result.acknowledge != nil {
			defer close(result.acknowledge)
		}
		if result.err != nil {
			return result.err
		}
		if beforeAcknowledge != nil {
			return beforeAcknowledge()
		}
		return nil
	}
}

func (proxy *configuredPTYTerminalProxy) Close() error {
	if proxy == nil {
		return nil
	}
	proxy.mu.Lock()
	if proxy.closed {
		proxy.mu.Unlock()
		return nil
	}
	proxy.closed = true
	proxy.mu.Unlock()
	os.Stdout = proxy.original
	closeErr := proxy.slave.Close()
	readErr := <-proxy.readDone
	masterErr := proxy.master.Close()
	readinessErr := proxy.readiness.Close()
	return errors.Join(closeErr, readErr, masterErr, readinessErr)
}

func newPTYCheckpointTerminalFile(file *os.File) *ptyCheckpointTerminalFile {
	if file == nil {
		panic("create PTY checkpoint terminal with nil file")
	}
	return &ptyCheckpointTerminalFile{
		file:   file,
		writer: checkpoint.NewWriter(file),
	}
}

func (file *ptyCheckpointTerminalFile) Write(payload []byte) (int, error) {
	return file.writer.Write(payload)
}

func (file *ptyCheckpointTerminalFile) Read(payload []byte) (int, error) {
	return file.file.Read(payload)
}

func (file *ptyCheckpointTerminalFile) Close() error {
	return file.file.Close()
}

func (file *ptyCheckpointTerminalFile) Fd() uintptr {
	return file.file.Fd()
}

func runPTYFixtureProcess(ctx context.Context, processConfig appfixture.ProcessConfig) (runErr error) {
	if err := processConfig.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(processConfig.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("create fixture workspace: %w", err)
	}
	if err := appfixture.PrepareConfigAndBinding(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot); err != nil {
		return err
	}

	terminal := newPTYCheckpointTerminalFile(os.Stdout)
	if err := terminal.writer.QueueBeforeNextWrite(checkpoint.KindScenarioStart, nil); err != nil {
		return fmt.Errorf("queue scenario-start checkpoint: %w", err)
	}
	var scenarioState *ptyCheckpointScenarioState
	runtime, err := appfixture.NewRuntime(processConfig.ScriptPath, func(
		targetFinalAssistantOrdinal appfixture.ScriptFinalAssistantOrdinal,
	) func(context.Context) error {
		scenarioState = newPTYCheckpointScenarioState(targetFinalAssistantOrdinal)
		return func(context.Context) error {
			if err := terminal.writer.Emit(checkpoint.KindScenarioComplete, nil); err != nil {
				return err
			}
			scenarioState.markScenarioComplete()
			return nil
		}
	})
	if err != nil {
		return err
	}
	if scenarioState == nil {
		panic("PTY fixture runtime did not configure scenario completion state")
	}
	defer func() {
		observationErr := appfixture.WriteObservation(
			processConfig.ObservationPath,
			runtime.Observation(runErr),
		)
		if observationErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write fixture observation: %w", observationErr))
		}
	}()

	sessionID, err := runtime.SeedSession(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot)
	if err != nil {
		return err
	}
	options := Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		SessionID:             sessionID,
		ConfigRoot:            processConfig.PersistenceRoot,
		OpenAIBaseURL:         "http://127.0.0.1:1/v1",
		OpenAIBaseURLExplicit: true,
		startupOptions:        runtime.StartupOptions(),
	}
	interactor := newInteractiveAuthInteractor()
	standingServer, err := startEmbeddedServer(ctx, options, interactor, true)
	if err != nil {
		return fmt.Errorf("start fixture server: %w", err)
	}
	defer func() { _ = standingServer.Close() }()

	server, err := startSessionServer(ctx, options, interactor, true)
	if err != nil {
		return err
	}
	boundServer, err := ensureInteractiveProjectBinding(ctx, server)
	if err != nil {
		_ = server.Close()
		return err
	}
	if shouldCloseReboundServer(server, boundServer) {
		defer func() { _ = boundServer.Close() }()
	}
	server = boundServer

	planner := newSessionLaunchPlanner(server)
	parsedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return err
	}
	launchRequest := sessionLaunchRequest{
		Mode:   launchModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(parsedSessionID),
	}
	plan, err := planner.PlanSession(ctx, launchRequest)
	if err != nil {
		return err
	}
	runtimePlan, request, err := prepareSessionUIRun(ctx, server, planner, plan, "", false, "", false)
	if err != nil {
		return err
	}
	defer runtimePlan.Close()
	composition, err := composeUIProgram(request, terminal)
	if err != nil {
		return err
	}
	wrapped := newPTYCheckpointModel(composition.model, terminal.writer, scenarioState)
	finalModel, err := runUIProgram(composition, wrapped)
	if err != nil {
		return err
	}
	finalWrapped, ok := finalModel.(*ptyCheckpointModel)
	if !ok {
		return fmt.Errorf("PTY fixture final model has unexpected type %T", finalModel)
	}
	if _, ok := finalWrapped.appModel(); !ok {
		return fmt.Errorf("PTY fixture inner final model has unexpected type %T", finalWrapped.inner)
	}
	return nil
}
