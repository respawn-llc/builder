package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	servercore "core/server/core"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"

	"golang.org/x/net/websocket"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type gatewayConcurrencyWorkflowService struct {
	apicontract.WorkflowService
	getWorkflowTask      func(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error)
	completeWorkflowTask func(context.Context, serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error)
	startWorkflowTask    func(context.Context, serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error)
	resumeWorkflowTask   func(context.Context, serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error)
}

func (s *gatewayConcurrencyWorkflowService) StartWorkflowTask(
	ctx context.Context,
	req serverapi.WorkflowTaskStartRequest,
) (serverapi.WorkflowTaskStartResponse, error) {
	if s.startWorkflowTask == nil {
		return s.WorkflowService.StartWorkflowTask(ctx, req)
	}
	return s.startWorkflowTask(ctx, req)
}

func (s *gatewayConcurrencyWorkflowService) ResumeWorkflowTask(
	ctx context.Context,
	req serverapi.WorkflowTaskResumeRequest,
) (serverapi.WorkflowTaskResumeResponse, error) {
	if s.resumeWorkflowTask == nil {
		return s.WorkflowService.ResumeWorkflowTask(ctx, req)
	}
	return s.resumeWorkflowTask(ctx, req)
}

func (s *gatewayConcurrencyWorkflowService) GetWorkflowTask(
	ctx context.Context,
	req serverapi.WorkflowTaskGetRequest,
) (serverapi.WorkflowTaskGetResponse, error) {
	return s.getWorkflowTask(ctx, req)
}

func (s *gatewayConcurrencyWorkflowService) CompleteWorkflowTask(
	ctx context.Context,
	req serverapi.WorkflowTaskCompleteRequest,
) (serverapi.WorkflowTaskCompleteResponse, error) {
	if s.completeWorkflowTask == nil {
		return s.WorkflowService.CompleteWorkflowTask(ctx, req)
	}
	return s.completeWorkflowTask(ctx, req)
}

type gatewayConcurrencyDependencies struct {
	GatewayDependencies
	workflow apicontract.WorkflowService
	debug    bool
}

type gatewayAutomaticFatalSteerer struct {
	cause error
}

func (s gatewayAutomaticFatalSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return nil, s.cause
}

func (gatewayAutomaticFatalSteerer) PrepareManualMoveAssignments(
	context.Context,
	[]workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, errors.New("Manual Move assignment preparation must not run")
}

type gatewayFailingRunner struct {
	cause error
}

func (r gatewayFailingRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	*workflowexecution.CurrentNodeClassifiedAssignment,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	return r.cause
}

type gatewayCommittedAssignmentSteerer struct{}

func (gatewayCommittedAssignmentSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return gatewayCommittedAssignment{}, nil
}

func (gatewayCommittedAssignmentSteerer) PrepareManualMoveAssignments(
	context.Context,
	[]workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, errors.New("Manual Move assignment preparation must not run")
}

type gatewayCommittedAssignment struct{}

func (gatewayCommittedAssignment) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}

func gatewayWorkflowStore(t *testing.T, appCore *servercore.Core) *workflowstore.Store {
	t.Helper()
	store, err := workflowstore.New(
		appCore.MetadataStore(),
		workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")),
	)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return store
}

func installCurrentNodeInterruptionFailure(t *testing.T, deps *servercore.Core) {
	t.Helper()
	if _, err := deps.MetadataStore().DB().ExecContext(context.Background(), `
CREATE TRIGGER current_node_interruption_failure
BEFORE UPDATE OF scheduling_state ON task_current_nodes
WHEN OLD.scheduling_state <> 'interrupted'
 AND NEW.scheduling_state = 'interrupted'
BEGIN
	SELECT RAISE(ABORT, 'current node interruption persistence failed');
END;
`); err != nil {
		t.Fatalf("install Current Node interruption SQL failure: %v", err)
	}
}

func requireGatewayResponse(t *testing.T, conn *websocket.Conn, requestID string) {
	t.Helper()
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive Gateway response %q: %v", requestID, err)
	}
	if response.ID != requestID || response.Error != nil {
		if response.Error != nil {
			t.Fatalf("Gateway response error = %+v, want successful response %q", *response.Error, requestID)
		}
		t.Fatalf("Gateway response = %+v, want successful response %q", response, requestID)
	}
}

func requireControllerPersistenceFailure(
	t *testing.T,
	controller *workflowexecution.CurrentNodeController,
	taskID workflow.TaskID,
	operationFailure error,
) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		err := controller.EnsureTaskQuiescent(taskID)
		if !errors.Is(err, operationFailure) {
			return false
		}
		var sqliteErr *sqlitedriver.Error
		return errors.As(err, &sqliteErr) &&
			sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER
	}, "controller did not expose the operation and real SQLite interruption failures")
}

func (d *gatewayConcurrencyDependencies) WorkflowClient() apicontract.WorkflowService {
	return d.workflow
}

func (d *gatewayConcurrencyDependencies) DebugEnabled() bool {
	return d.debug
}

type gatewayCloseTracker struct {
	active             atomic.Int32
	entered            chan struct{}
	canceled           chan string
	exited             chan string
	allowActivationEnd chan struct{}
}

type gatewayCloseWorkflowService struct {
	apicontract.WorkflowService
	tracker *gatewayCloseTracker
}

func (s *gatewayCloseWorkflowService) GetWorkflowTask(ctx context.Context, _ serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	s.tracker.active.Add(1)
	s.tracker.entered <- struct{}{}
	defer func() {
		s.tracker.active.Add(-1)
		s.tracker.exited <- "workflow"
	}()
	<-ctx.Done()
	s.tracker.canceled <- "workflow"
	return serverapi.WorkflowTaskGetResponse{}, ctx.Err()
}

type gatewayCloseRuntimeService struct {
	apicontract.SessionRuntimeService
	tracker        *gatewayCloseTracker
	releaseStarted chan gatewayCloseRelease
}

type gatewayCloseRelease struct {
	request serverapi.SessionRuntimeReleaseRequest
	active  int32
}

func (s *gatewayCloseRuntimeService) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	s.tracker.active.Add(1)
	s.tracker.entered <- struct{}{}
	defer func() {
		s.tracker.active.Add(-1)
		s.tracker.exited <- "runtime"
	}()
	<-ctx.Done()
	s.tracker.canceled <- "runtime"
	<-s.tracker.allowActivationEnd
	return serverapi.SessionRuntimeActivateResponse{
		Attachment: serverapi.SessionRuntimeAttachment{SessionID: req.SessionID, Generation: 1},
	}, nil
}

func (s *gatewayCloseRuntimeService) ReleaseSessionRuntime(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	s.releaseStarted <- gatewayCloseRelease{request: req, active: s.tracker.active.Load()}
	return serverapi.SessionRuntimeReleaseResponse{Released: true}, nil
}

type gatewayCloseDependencies struct {
	GatewayDependencies
	workflow apicontract.WorkflowService
	runtime  apicontract.SessionRuntimeService
}

func (d *gatewayCloseDependencies) WorkflowClient() apicontract.WorkflowService {
	return d.workflow
}

func (d *gatewayCloseDependencies) SessionRuntimeClient() apicontract.SessionRuntimeService {
	return d.runtime
}

func TestGatewayConcurrentUnaryResponsesAreCorrelated(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()

	blockedEntered := make(chan struct{})
	releaseBlocked := make(chan struct{})
	var releaseBlockedOnce sync.Once
	release := func() {
		releaseBlockedOnce.Do(func() { close(releaseBlocked) })
	}
	workflow := &gatewayConcurrencyWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		getWorkflowTask: func(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
			if req.TaskID == "task-1" {
				close(blockedEntered)
				select {
				case <-releaseBlocked:
				case <-ctx.Done():
					return serverapi.WorkflowTaskGetResponse{}, ctx.Err()
				}
			}
			return serverapi.WorkflowTaskGetResponse{}, errors.New("blocked workflow task lookup")
		},
	}
	deps := &gatewayConcurrencyDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
	}
	gateway, err := NewGateway(deps, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	t.Cleanup(release)

	sendGatewayRequest(t, conn, "blocked", protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{TaskID: "task-1"})
	select {
	case <-blockedEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked workflow task lookup")
	}
	sendGatewayRequest(t, conn, "fast", protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{TaskID: "task-2"})

	if err := conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive fast response: %v", err)
	}
	if response.ID != "fast" {
		t.Fatalf("first response id = %q, want fast", response.ID)
	}
	if response.Error == nil {
		t.Fatal("fast response unexpectedly succeeded")
	}

	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline: %v", err)
	}
	release()
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive blocked response: %v", err)
	}
	if response.ID != "blocked" {
		t.Fatalf("second response id = %q, want blocked", response.ID)
	}
}

func TestGatewayOrdinaryHandlerPanicPropagates(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()

	panicCause := errors.New("gateway debug test panic")
	workflow := &gatewayConcurrencyWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		getWorkflowTask: func(_ context.Context, _ serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
			panic(panicCause)
		},
	}
	deps := &gatewayConcurrencyDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
		debug:               true,
	}
	gateway, err := NewGateway(deps, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	req := protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "panic",
		Method:  protocol.MethodWorkflowTaskGet,
		Params:  mustJSON(t, serverapi.WorkflowTaskGetRequest{TaskID: "panic"}),
	}
	state := &connectionState{handshakeDone: true}
	stopped := false
	defer func() {
		recovered := recover()
		cause, ok := recovered.(error)
		if !ok || !errors.Is(cause, panicCause) {
			t.Fatalf("recovered panic = %#v, want original panic cause", recovered)
		}
		if stopped {
			t.Fatal("ordinary request stop callback ran after panic")
		}
	}()
	gateway.serveOrdinaryGatewayRequest(nil, context.Background(), state, req, gatewayRequestSchedule{
		kind: gatewayRequestScheduleOrdinary,
	}, func() {
		stopped = true
	})
}

func TestGatewayAutomaticSuccessorFatalTerminatesProcess(t *testing.T) {
	const childEnv = "KENT_GATEWAY_AUTOMATIC_FATAL_CHILD"
	const addressEnv = "KENT_GATEWAY_AUTOMATIC_FATAL_ADDRESS"
	addressPath := os.Getenv(addressEnv)
	if addressPath == "" {
		addressPath = filepath.Join(t.TempDir(), "gateway-address")
	}
	if os.Getenv(childEnv) == "1" {
		appCore, _ := newGatewayTestCore(t, true, true)
		defer func() { _ = appCore.Close() }()
		task := createGatewaySearchableTask(t, appCore)
		taskID := workflow.TaskID(task.ID)
		workflowStore := gatewayWorkflowStore(t, appCore)
		if _, err := workflowStore.StartTask(context.Background(), taskID); err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		installCurrentNodeInterruptionFailure(t, appCore)
		authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
		controller, err := workflowexecution.NewCurrentNodeController(
			workflowStore,
			gatewayFailingRunner{cause: errors.New("automatic fatal test runner must not start")},
			authority,
			workflowexecution.NewTaskMutationCoordinator(),
			workflowexecution.CurrentNodeControllerConfig{
				AgentConcurrency: 1,
				AssignmentSteerer: gatewayAutomaticFatalSteerer{
					cause: errors.New("automatic successor assignment failed"),
				},
			},
		)
		if err != nil {
			t.Fatalf("NewCurrentNodeController: %v", err)
		}
		workflowClient := &gatewayConcurrencyWorkflowService{
			WorkflowService: appCore.WorkflowClient(),
			completeWorkflowTask: func(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
				_, completeErr := controller.CompleteIdleCurrentNode(
					ctx,
					workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
					req.TransitionID,
					req.OutputValues,
					req.Commentary,
				)
				return serverapi.WorkflowTaskCompleteResponse{}, completeErr
			},
		}
		gateway, err := NewGateway(
			&gatewayConcurrencyDependencies{GatewayDependencies: appCore, workflow: workflowClient},
			gatewayTestIdentity(),
		)
		if err != nil {
			t.Fatalf("NewGateway: %v", err)
		}
		server := httptest.NewServer(gateway.Handler())
		if err := os.WriteFile(addressPath, []byte(server.URL), 0o600); err != nil {
			t.Fatalf("write Gateway address: %v", err)
		}
		conn := dialGateway(t, server)
		handshakeGateway(t, conn)
		sendGatewayRequest(t, conn, "fatal", protocol.MethodWorkflowTaskComplete, serverapi.WorkflowTaskCompleteRequest{
			TaskID:       task.ID,
			TransitionID: "done",
			ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
			Force:        true,
		})
		select {}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGatewayAutomaticSuccessorFatalTerminatesProcess$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), childEnv+"=1", addressEnv+"="+addressPath)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("automatic successor fatal child did not terminate\n%s", output)
	}
	if err == nil {
		t.Fatalf("automatic successor fatal child exited successfully\n%s", output)
	}
	rawAddress, readErr := os.ReadFile(addressPath)
	if readErr != nil {
		t.Fatalf("read Gateway address after child exit: %v\n%s", readErr, output)
	}
	if _, dialErr := websocket.Dial(
		"ws"+string(rawAddress[len("http"):]),
		"",
		string(rawAddress),
	); dialErr == nil {
		t.Fatal("Gateway accepted a subsequent connection after process-fatal panic")
	}
}

func TestGatewayExplicitAdmissionInterruptionPersistenceFailureRemainsNonFatal(t *testing.T) {
	for _, test := range []struct {
		name   string
		resume bool
	}{
		{name: "initial Start"},
		{name: "explicit Resume", resume: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			appCore, _ := newGatewayTestCore(t, true, true)
			defer func() { _ = appCore.Close() }()
			task := createGatewaySearchableTask(t, appCore)
			taskID := workflow.TaskID(task.ID)
			workflowStore := gatewayWorkflowStore(t, appCore)
			operationFailure := errors.New("explicit admission failed")
			var steerer workflowexecution.CurrentNodeAssignmentSteerer = gatewayAutomaticFatalSteerer{
				cause: operationFailure,
			}
			runner := gatewayFailingRunner{cause: errors.New("runner must not start")}
			if test.resume {
				started, err := workflowStore.StartTask(context.Background(), taskID)
				if err != nil {
					t.Fatalf("StartTask: %v", err)
				}
				if err := workflowStore.InterruptCurrentNode(
					context.Background(),
					started.Mutation.Created[0].Reference,
					workflow.CurrentNodeInterruptionReasonUserInterrupt,
					workflow.NewCurrentNodeInterruptionDetail(
						string(workflow.CurrentNodeInterruptionReasonUserInterrupt),
						nil,
					),
				); err != nil {
					t.Fatalf("seed interrupted Current Node: %v", err)
				}
				steerer = gatewayCommittedAssignmentSteerer{}
				runner = gatewayFailingRunner{cause: operationFailure}
			}
			installCurrentNodeInterruptionFailure(t, appCore)
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
			controller, err := workflowexecution.NewCurrentNodeController(
				workflowStore,
				runner,
				authority,
				workflowexecution.NewTaskMutationCoordinator(),
				workflowexecution.CurrentNodeControllerConfig{
					AgentConcurrency:  1,
					AssignmentSteerer: steerer,
				},
			)
			if err != nil {
				t.Fatalf("NewCurrentNodeController: %v", err)
			}
			defer func() { _ = authority.Close(context.Background()) }()

			workflowClient := &gatewayConcurrencyWorkflowService{
				WorkflowService: appCore.WorkflowClient(),
				startWorkflowTask: func(ctx context.Context, _ serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
					started, startErr := controller.StartTask(
						ctx,
						taskID,
						workflowexecution.TaskStartPreparation{
							Prepare: func(context.Context) error { return nil },
							Commit:  func(context.Context) error { return nil },
						},
						func(workflowexecution.TaskPreparationFinalization) {},
					)
					if len(started.Mutation.Created) == 0 {
						return serverapi.WorkflowTaskStartResponse{}, startErr
					}
					response := serverapi.WorkflowTaskStartResponse{
						Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
						Applied: &serverapi.WorkflowTaskStartApplied{
							CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{
								NodeID: string(started.Mutation.Created[0].Reference.NodeID),
							}},
						},
					}
					return response, startErr
				},
				resumeWorkflowTask: func(ctx context.Context, _ serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
					resumed, resumeErr := controller.ResumeTask(ctx, taskID)
					if len(resumed.CurrentNodes) == 0 {
						return serverapi.WorkflowTaskResumeResponse{}, resumeErr
					}
					response := serverapi.WorkflowTaskResumeResponse{
						Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
						Applied: &serverapi.WorkflowTaskResumeApplied{
							CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{
								NodeID: string(resumed.CurrentNodes[0].Reference.NodeID),
							}},
						},
					}
					return response, resumeErr
				},
			}
			gateway, err := NewGateway(
				&gatewayConcurrencyDependencies{GatewayDependencies: appCore, workflow: workflowClient},
				gatewayTestIdentity(),
			)
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}
			server := httptest.NewServer(gateway.Handler())
			defer server.Close()
			conn := dialGateway(t, server)
			handshakeGateway(t, conn)
			if test.resume {
				sendGatewayRequest(t, conn, "explicit", protocol.MethodWorkflowTaskResume, serverapi.WorkflowTaskResumeRequest{
					TaskID:           task.ID,
					SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
				})
			} else {
				sendGatewayRequest(t, conn, "explicit", protocol.MethodWorkflowTaskStart, serverapi.WorkflowTaskStartRequest{
					TaskID:           task.ID,
					SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
				})
			}
			requireGatewayResponse(t, conn, "explicit")
			_ = conn.Close()

			requireControllerPersistenceFailure(t, controller, taskID, operationFailure)
			next := dialGateway(t, server)
			handshakeGateway(t, next)
			_ = next.Close()

			_ = controller.Close()
		})
	}
}

func TestGatewayCloseCancelsAndDrainsHandlersBeforeRuntimeCleanup(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()
	store := createGatewayAuthoritativeSession(t, appCore)

	tracker := &gatewayCloseTracker{
		entered:            make(chan struct{}, 2),
		canceled:           make(chan string, 2),
		exited:             make(chan string, 2),
		allowActivationEnd: make(chan struct{}),
	}
	var allowActivationEndOnce sync.Once
	allowActivationEnd := func() {
		allowActivationEndOnce.Do(func() { close(tracker.allowActivationEnd) })
	}
	runtime := &gatewayCloseRuntimeService{
		SessionRuntimeService: appCore.SessionRuntimeClient(),
		tracker:               tracker,
		releaseStarted:        make(chan gatewayCloseRelease, 1),
	}
	workflow := &gatewayCloseWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		tracker:         tracker,
	}
	deps := &gatewayCloseDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
		runtime:             runtime,
	}
	gateway, err := NewGateway(deps, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	t.Cleanup(allowActivationEnd)

	sendGatewayRequest(t, conn, "workflow", protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{TaskID: "task-1"})
	sendGatewayRequest(t, conn, "runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "runtime"))
	for i := 0; i < 2; i++ {
		select {
		case <-tracker.entered:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for admitted handler %d", i+1)
		}
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	canceled := make(map[string]struct{}, 2)
	for len(canceled) < 2 {
		select {
		case name := <-tracker.canceled:
			canceled[name] = struct{}{}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for canceled handlers")
		}
	}
	if _, ok := canceled["workflow"]; !ok {
		t.Fatal("workflow handler context was not canceled")
	}
	if _, ok := canceled["runtime"]; !ok {
		t.Fatal("runtime handler context was not canceled")
	}
	waitForGatewayCloseEvent(t, tracker.exited, "workflow")
	if got := tracker.active.Load(); got != 1 {
		t.Fatalf("active handler count after cancellation = %d, want runtime handler still admitted", got)
	}

	allowActivationEnd()
	waitForGatewayCloseEvent(t, tracker.exited, "runtime")
	select {
	case release := <-runtime.releaseStarted:
		if release.request.Attachment.SessionID != store.Meta().SessionID || release.request.Attachment.Generation != 1 {
			t.Fatalf("cleanup attachment = %+v, want session %q generation 1", release.request.Attachment, store.Meta().SessionID)
		}
		if !release.request.DropOwner || release.request.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle {
			t.Fatalf("cleanup release request = %+v, want owner drop close-if-idle", release.request)
		}
		if release.active != 0 {
			t.Fatalf("cleanup started with %d active handler(s), want 0", release.active)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime cleanup")
	}
	if got := tracker.active.Load(); got != 0 {
		t.Fatalf("active handler count after cleanup started = %d, want 0", got)
	}
}

func waitForGatewayCloseEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	for {
		select {
		case got := <-events:
			if got == want {
				return
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s handler exit", want)
		}
	}
}

func TestGatewayAdmissionCapsOrdinaryUnaryRequests(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	defer func() { _ = appCore.Close() }()

	entered := make(chan string, 17)
	entered17BeforeRelease := make(chan struct{}, 1)
	release := make(map[string]chan struct{}, 17)
	for i := int32(1); i <= 17; i++ {
		release["task-"+stringID(i)] = make(chan struct{}, 1)
	}
	var capacityReleased atomic.Bool
	var calls atomic.Int32
	workflow := &gatewayConcurrencyWorkflowService{
		WorkflowService: appCore.WorkflowClient(),
		getWorkflowTask: func(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
			calls.Add(1)
			if req.TaskID == "task-"+stringID(17) && !capacityReleased.Load() {
				entered17BeforeRelease <- struct{}{}
			}
			entered <- req.TaskID
			select {
			case <-release[req.TaskID]:
			case <-ctx.Done():
				return serverapi.WorkflowTaskGetResponse{}, ctx.Err()
			}
			return serverapi.WorkflowTaskGetResponse{}, errors.New("blocked workflow task lookup")
		},
	}
	deps := &gatewayConcurrencyDependencies{
		GatewayDependencies: appCore,
		workflow:            workflow,
	}
	gateway, err := NewGateway(deps, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	releaseAll := func() {
		for id := int32(1); id <= 17; id++ {
			select {
			case release["task-"+stringID(id)] <- struct{}{}:
			default:
			}
		}
	}
	t.Cleanup(releaseAll)

	for i := int32(1); i <= 17; i++ {
		sendGatewayRequest(t, conn, stringID(i), protocol.MethodWorkflowTaskGet, serverapi.WorkflowTaskGetRequest{
			TaskID: "task-" + stringID(i),
		})
	}

	enteredIDs := make(map[string]struct{}, 16)
	for len(enteredIDs) < 16 {
		select {
		case got := <-entered:
			if got == "task-request-17" {
				t.Fatal("17th request entered before an admission slot was released")
			}
			enteredIDs[got] = struct{}{}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for the first 16 service calls")
		}
	}
	if got := calls.Load(); got != 16 {
		t.Fatalf("service call count = %d, want 16", got)
	}
	for i := int32(1); i <= 16; i++ {
		if _, ok := enteredIDs["task-"+stringID(i)]; !ok {
			t.Fatalf("missing admitted request %q", stringID(i))
		}
	}
	select {
	case got := <-entered:
		t.Fatalf("unexpected extra service call %q before capacity release", got)
	default:
	}

	capacityReleased.Store(true)
	release["task-"+stringID(1)] <- struct{}{}
	select {
	case got := <-entered:
		if got != "task-request-17" {
			t.Fatalf("service call after release = %q, want request-17", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the 17th request to enter")
	}
	select {
	case <-entered17BeforeRelease:
		t.Fatal("17th request entered before capacity release")
	default:
	}

	for id := range enteredIDs {
		release[id] <- struct{}{}
	}
	release["task-"+stringID(17)] <- struct{}{}
	responses := make(map[string]struct{}, 17)
	for i := 0; i < 17; i++ {
		var response protocol.Response
		if err := websocket.JSON.Receive(conn, &response); err != nil {
			t.Fatalf("receive response %d: %v", i+1, err)
		}
		if response.Error == nil {
			t.Fatalf("response %q unexpectedly succeeded", response.ID)
		}
		responses[response.ID] = struct{}{}
	}
	for i := int32(1); i <= 17; i++ {
		if _, ok := responses[stringID(i)]; !ok {
			t.Fatalf("missing response for request %q", stringID(i))
		}
	}
}

func stringID(id int32) string {
	return "request-" + strconv.FormatInt(int64(id), 10)
}

func sendGatewayRequest(t *testing.T, conn *websocket.Conn, id, method string, params any) {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      id,
		Method:  method,
		Params:  mustJSON(t, params),
	}); err != nil {
		t.Fatalf("send %s: %v", id, err)
	}
}
