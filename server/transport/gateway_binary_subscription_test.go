package transport

import (
	"context"
	"io"
	"testing"

	"core/shared/apicontract"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	workflowdefinitionpb "core/shared/protoapi/gen/kent/api/workflow_definition"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestGatewayBinarySubscriptionScheduleAndServeWorktreeSetup(t *testing.T) {
	setupID := worktreecontract.NewSetupOperationID()
	tests := []struct {
		name     string
		terminal error
	}{
		{name: "normal completion", terminal: io.EOF},
		{name: "stream failure", terminal: serverapi.ErrStreamFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subscription := &scriptedWorktreeSetupSubscription{
				events: []worktreecontract.SetupEvent{
					{
						SetupOperationID: setupID,
						Phase:            worktreecontract.SetupPhaseStarted,
						Started: &worktreecontract.SetupStarted{
							SourceWorkspaceRoot: "/workspace",
							WorktreeRoot:        "/worktree",
							ScriptPath:          "/workspace/setup.sh",
						},
					},
					{
						SetupOperationID: setupID,
						Phase:            worktreecontract.SetupPhaseCompleted,
						Completed:        &worktreecontract.SetupCompleted{},
					},
				},
				terminal: test.terminal,
			}
			service := &worktreeSetupGatewayService{subscription: subscription}
			registration, err := productionGatewayRegistration()
			if err != nil {
				t.Fatalf("production Gateway registration: %v", err)
			}
			registration.subscriptions = make(map[string]gatewayBinarySubscriptionBinding, 1)
			if err := registerWorktreeSetupGatewayBinaryBinding(registration.subscriptions); err != nil {
				t.Fatalf("register Worktree setup binding: %v", err)
			}
			subscribeOperation := gatewayOperationName(t, worktreeSetupMethod(t, "Subscribe"))
			binding := registration.subscriptions[subscribeOperation]
			payload, err := protoapi.Encode(&worktreepb.SetupSubscribeRequest{
				SetupOperationId: setupID.String(),
			})
			if err != nil {
				t.Fatalf("encode setup request: %v", err)
			}
			correlation := "setup"
			request := gatewayEstablishedRequest{
				binarySubscription: &gatewayBinarySubscriptionRequest{
					binding: binding,
					call: &sharedpb.Call{
						Operation:   subscribeOperation,
						Correlation: &correlation,
						Payload:     payload,
					},
				},
			}
			gateway := &Gateway{
				deps:         &worktreeSetupGatewayDependencies{worktree: service},
				registration: registration,
			}
			schedule := gateway.gatewayRequestScheduleForEstablished(request)
			if schedule.kind != gatewayRequestScheduleSubscription {
				t.Fatalf("schedule = %d, want subscription", schedule.kind)
			}
			conn := &recordingGatewayConn{}
			if gateway.serveEstablishedRequest(
				conn,
				context.Background(),
				&connectionState{handshakeDone: true},
				request,
				schedule,
			) {
				t.Fatal("binary subscription unexpectedly kept the connection loop active")
			}
			if !subscription.closed {
				t.Fatal("setup subscription was not closed")
			}
			if service.request.SetupOperationID != setupID {
				t.Fatalf("setup request = %+v, want %s", service.request, setupID)
			}
			assertWorktreeSetupFrames(t, conn.frames, correlation, test.terminal)
		})
	}
}

func TestGatewayBinarySubscriptionDirectionResolution(t *testing.T) {
	registration, err := productionGatewayRegistration()
	if err != nil {
		t.Fatalf("production Gateway registration: %v", err)
	}
	registration.subscriptions = make(map[string]gatewayBinarySubscriptionBinding, 1)
	if err := registerWorktreeSetupGatewayBinaryBinding(registration.subscriptions); err != nil {
		t.Fatalf("register Worktree setup binding: %v", err)
	}
	gateway := &Gateway{registration: registration}
	tests := []struct {
		name      string
		operation string
		want      sharedpb.TransportFailureCode
	}{
		{
			name:      "Worktree setup event",
			operation: gatewayOperationName(t, worktreeSetupMethod(t, "Event")),
			want:      sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_WRONG_DIRECTION,
		},
		{
			name:      "Worktree setup completion",
			operation: gatewayOperationName(t, worktreeSetupMethod(t, "Complete")),
			want:      sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_WRONG_DIRECTION,
		},
		{
			name: "still-JSON notification",
			operation: gatewayOperationName(
				t,
				workflowdefinitionpb.File_kent_api_workflow_definition_workflow_definition_proto.
					Services().ByName("WorkflowSubscriptionService").Methods().ByName("Event"),
			),
			want: sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_UNKNOWN_OPERATION,
		},
		{
			name:      "unknown operation",
			operation: "kent.api.missing.missing_service.missing",
			want:      sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_UNKNOWN_OPERATION,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
				Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{Operation: test.operation}},
			})
			if err != nil {
				t.Fatalf("encode call: %v", err)
			}
			_, _, failure := gateway.resolveBinaryRequest(encoded)
			if failure == nil || failure.Code != test.want {
				t.Fatalf("failure = %+v, want %s", failure, test.want)
			}
		})
	}
}

func TestGatewayBinarySubscriptionRegistration(t *testing.T) {
	registration := worktreeSetupSubscriptionRegistration(t)
	if err := registration.validateAuthorityPartition(nil); err != nil {
		t.Fatalf("validate setup subscription authority: %v", err)
	}
}

func TestGatewayBinarySubscriptionRegistrationRejectsInvalidBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gatewayRegistration)
	}{
		{
			name: "executable descriptor",
			mutate: func(registration *gatewayRegistration) {
				subscribe := worktreeSetupMethod(t, "Subscribe")
				binding := registration.subscriptions[gatewayOperationName(t, subscribe)]
				binding.operation = registration.operations[gatewayOperationName(t, worktreeSetupMethod(t, "Event"))]
				registration.subscriptions[gatewayOperationName(t, subscribe)] = binding
			},
		},
		{
			name: "request type",
			mutate: func(registration *gatewayRegistration) {
				subscribe := worktreeSetupMethod(t, "Subscribe")
				binding := registration.subscriptions[gatewayOperationName(t, subscribe)]
				binding.request = func() proto.Message { return &worktreepb.SetupCompletion{} }
				registration.subscriptions[gatewayOperationName(t, subscribe)] = binding
			},
		},
		{
			name: "start result type",
			mutate: func(registration *gatewayRegistration) {
				subscribe := worktreeSetupMethod(t, "Subscribe")
				binding := registration.subscriptions[gatewayOperationName(t, subscribe)]
				binding.start = func() (proto.Message, error) { return &worktreepb.SetupEvent{}, nil }
				registration.subscriptions[gatewayOperationName(t, subscribe)] = binding
			},
		},
		{
			name: "event kind",
			mutate: func(registration *gatewayRegistration) {
				mutateWorktreeSetupOperationOptions(t, registration, "Event", func(options *sharedpb.KentMethodOptions) {
					options.Kind = sharedpb.OperationKind_OPERATION_KIND_UNARY
				})
			},
		},
		{
			name: "event direction",
			mutate: func(registration *gatewayRegistration) {
				mutateWorktreeSetupOperationOptions(t, registration, "Event", func(options *sharedpb.KentMethodOptions) {
					options.Direction = sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER
				})
			},
		},
		{
			name: "event payload type",
			mutate: func(registration *gatewayRegistration) {
				subscribe := worktreeSetupMethod(t, "Subscribe")
				binding := registration.subscriptions[gatewayOperationName(t, subscribe)]
				binding.event = func() proto.Message { return &worktreepb.SetupCompletion{} }
				registration.subscriptions[gatewayOperationName(t, subscribe)] = binding
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registration := worktreeSetupSubscriptionRegistration(t)
			test.mutate(&registration)
			if err := registration.validateAuthorityPartition(nil); err == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
}

func assertWorktreeSetupFrames(
	t *testing.T,
	frames []rpcwire.Frame,
	correlation string,
	terminal error,
) {
	t.Helper()
	if len(frames) != 4 {
		t.Fatalf("frame count = %d, want start result, two events, and completion", len(frames))
	}
	subscribeName := gatewayOperationName(t, worktreeSetupMethod(t, "Subscribe"))
	eventName := gatewayOperationName(t, worktreeSetupMethod(t, "Event"))
	completionName := gatewayOperationName(t, worktreeSetupMethod(t, "Complete"))
	for index, frame := range frames {
		envelope, err := protoapi.DecodeEnvelope(frame.Payload)
		if err != nil {
			t.Fatalf("decode frame %d: %v", index, err)
		}
		switch index {
		case 0:
			result := envelope.GetResult()
			if result == nil || result.Operation != subscribeName || result.GetCorrelation() != correlation {
				t.Fatalf("start result = %+v", result)
			}
			start := &worktreepb.SetupStartResult{}
			if err := protoapi.Decode(result.Payload, start); err != nil {
				t.Fatalf("decode start result: %v", err)
			}
			if start.GetSuccess() == nil {
				t.Fatalf("start result = %+v, want success", start)
			}
		case 1, 2:
			notification := envelope.GetNotificationEvent()
			if notification == nil || notification.Operation != eventName {
				t.Fatalf("event notification = %+v", notification)
			}
			event := &worktreepb.SetupEvent{}
			if err := protoapi.Decode(notification.Payload, event); err != nil {
				t.Fatalf("decode setup event: %v", err)
			}
			if index == 1 && event.GetStarted() == nil {
				t.Fatalf("first setup event = %+v, want started", event)
			}
			if index == 2 && event.GetCompleted() == nil {
				t.Fatalf("second setup event = %+v, want completed", event)
			}
		case 3:
			notification := envelope.GetNotificationEvent()
			if notification == nil || notification.Operation != completionName {
				t.Fatalf("completion notification = %+v", notification)
			}
			completion := &worktreepb.SetupCompletion{}
			if err := protoapi.Decode(notification.Payload, completion); err != nil {
				t.Fatalf("decode setup completion: %v", err)
			}
			want := streamCompleteParams(terminal)
			if want.Code == 0 {
				if completion.Code != nil || completion.Diagnostic != nil {
					t.Fatalf("normal completion = %+v, want empty", completion)
				}
			} else if completion.GetCode() != int32(want.Code) ||
				completion.GetDiagnostic() != want.Message {
				t.Fatalf(
					"error completion = %+v, want code=%d diagnostic=%q",
					completion,
					want.Code,
					want.Message,
				)
			}
		}
	}
}

type scriptedWorktreeSetupSubscription struct {
	events   []worktreecontract.SetupEvent
	terminal error
	closed   bool
}

func (s *scriptedWorktreeSetupSubscription) Next(context.Context) (worktreecontract.SetupEvent, error) {
	if len(s.events) == 0 {
		return worktreecontract.SetupEvent{}, s.terminal
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *scriptedWorktreeSetupSubscription) Close() error {
	s.closed = true
	return nil
}

type worktreeSetupGatewayService struct {
	apicontract.WorktreeService
	request      worktreecontract.SetupSubscribeRequest
	subscription worktreecontract.SetupSubscription
}

func (s *worktreeSetupGatewayService) SubscribeWorktreeSetup(
	_ context.Context,
	request worktreecontract.SetupSubscribeRequest,
) (worktreecontract.SetupSubscription, error) {
	s.request = request
	return s.subscription, nil
}

type worktreeSetupGatewayDependencies struct {
	GatewayDependencies
	worktree apicontract.WorktreeService
}

func (d *worktreeSetupGatewayDependencies) WorktreeClient() apicontract.WorktreeService {
	return d.worktree
}

func (*worktreeSetupGatewayDependencies) ServerAuthRequired() bool {
	return false
}

func worktreeSetupSubscriptionRegistration(t *testing.T) gatewayRegistration {
	t.Helper()
	operations := make(map[string]protoapi.Operation, 3)
	for _, methodName := range []string{"Subscribe", "Event", "Complete"} {
		method := worktreeSetupMethod(t, methodName)
		operation, err := protoapi.OperationFromDescriptor(method)
		if err != nil {
			t.Fatalf("resolve SetupService.%s: %v", methodName, err)
		}
		operation.LegacyWireName = nil
		operations[operation.Name] = operation
	}
	subscriptions := make(map[string]gatewayBinarySubscriptionBinding, 1)
	if err := registerWorktreeSetupGatewayBinaryBinding(subscriptions); err != nil {
		t.Fatalf("register Worktree setup binding: %v", err)
	}
	return gatewayRegistration{
		operations:    operations,
		legacy:        map[string]apicontract.Route{},
		binary:        map[string]gatewayBinaryBinding{},
		subscriptions: subscriptions,
	}
}

func worktreeSetupMethod(t *testing.T, name string) protoreflect.MethodDescriptor {
	t.Helper()
	service := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("SetupService")
	if service == nil {
		t.Fatal("generated SetupService descriptor is required")
	}
	method := service.Methods().ByName(protoreflect.Name(name))
	if method == nil {
		t.Fatalf("generated SetupService.%s descriptor is required", name)
	}
	return method
}

func mutateWorktreeSetupOperationOptions(
	t *testing.T,
	registration *gatewayRegistration,
	methodName string,
	mutate func(*sharedpb.KentMethodOptions),
) {
	t.Helper()
	name := gatewayOperationName(t, worktreeSetupMethod(t, methodName))
	operation := registration.operations[name]
	operation.Options = proto.Clone(operation.Options).(*sharedpb.KentMethodOptions)
	mutate(operation.Options)
	registration.operations[name] = operation
}
