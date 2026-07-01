package runtimewire

import (
	"context"
	"fmt"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/shared/clientui"
	"core/shared/invariant"
)

type RuntimeActivityPublisher interface {
	PublishRuntimeActivitySnapshot(sessionID string, snapshot runtimeactivity.ResponseSnapshot)
}

type RuntimeActivityRegistrySnapshotProvider interface {
	RuntimeActivityRegistrySnapshot(sessionID string) runtimeactivity.RegistrySnapshot
}

type RuntimeReadModelSnapshotProvider interface {
	RuntimeReadModelSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error)
}

func NewStepLifecycleSink(sessionID string, publisher RuntimeActivityPublisher) runtime.StepLifecycleSink {
	return NewStepLifecycleSinkWithInvariantPolicy(sessionID, publisher, invariant.NewPolicy())
}

func NewStepLifecycleSinkWithInvariantPolicy(sessionID string, publisher RuntimeActivityPublisher, policy invariant.Policy) runtime.StepLifecycleSink {
	if publisher == nil {
		return nil
	}
	return stepLifecycleSink{sessionID: strings.TrimSpace(sessionID), publisher: publisher, policy: policy}
}

type stepLifecycleSink struct {
	sessionID string
	publisher RuntimeActivityPublisher
	policy    invariant.Policy
}

func (s stepLifecycleSink) StepBegan(_ context.Context, snapshot runtime.StepLifecycleSnapshot) error {
	active := &runtimeactivity.ActiveStepSnapshot{
		RunID:      snapshot.RunID,
		StepID:     snapshot.StepID,
		ActiveKind: runtimeactivity.MustClientActiveKindFromRuntime(snapshot.ActiveKind),
	}
	return s.publish("step_began", false, runtimeactivity.ResolverSnapshot{
		Registry: s.registrySnapshot(),
		Active:   active,
	})
}

func (s stepLifecycleSink) StepEnded(_ context.Context, _ runtime.StepLifecycleSnapshot) error {
	return s.publish("step_ended", true, runtimeactivity.ResolverSnapshot{
		Registry: s.registrySnapshot(),
	})
}

func (s stepLifecycleSink) registrySnapshot() runtimeactivity.RegistrySnapshot {
	if provider, ok := s.publisher.(RuntimeActivityRegistrySnapshotProvider); ok {
		return provider.RuntimeActivityRegistrySnapshot(s.sessionID)
	}
	return runtimeactivity.RegistrySnapshot{Registered: true, QueueAccepting: true}
}

func (s stepLifecycleSink) publish(cause string, terminal bool, resolver runtimeactivity.ResolverSnapshot) error {
	sessionID := strings.TrimSpace(s.sessionID)
	snapshot, err := s.responseSnapshot(sessionID, resolver)
	if err != nil {
		return s.handlePublicationFailure(cause, terminal, resolver, runtimeactivity.ResponseSnapshot{}, err)
	}
	if snapshot.Activity.State == clientui.RuntimeActivityUnavailable {
		snapshot.Activity = clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	}
	if err := s.publishSnapshot(sessionID, snapshot); err != nil {
		return s.handlePublicationFailure(cause, terminal, resolver, snapshot, err)
	}
	return nil
}

func (s stepLifecycleSink) responseSnapshot(sessionID string, resolver runtimeactivity.ResolverSnapshot) (runtimeactivity.ResponseSnapshot, error) {
	if provider, ok := s.publisher.(RuntimeReadModelSnapshotProvider); ok {
		return provider.RuntimeReadModelSnapshot(context.Background(), sessionID, nil)
	}
	return runtimeactivity.BuildResponseSnapshot(sessionID, resolver)
}

func (s stepLifecycleSink) publishSnapshot(sessionID string, snapshot runtimeactivity.ResponseSnapshot) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("publish runtime activity snapshot panicked: %v", recovered)
		}
	}()
	s.publisher.PublishRuntimeActivitySnapshot(sessionID, snapshot)
	return nil
}

func (s stepLifecycleSink) handlePublicationFailure(cause string, terminal bool, resolver runtimeactivity.ResolverSnapshot, proposed runtimeactivity.ResponseSnapshot, failure error) error {
	sessionID := strings.TrimSpace(s.sessionID)
	s.policy.Check(false, invariant.ReadModelPublicationDiagnostic(invariant.ReadModelPublicationDiagnosticInput{
		Operation:                "runtime_step_lifecycle",
		SessionID:                sessionID,
		PublicationCause:         cause,
		ProposedReadModelVersion: fmt.Sprintf("%+v", proposed.Version),
		ResolverInputs:           fmt.Sprintf("%+v", resolver),
		ResolvedProposedActivity: fmt.Sprintf("%+v", proposed.Activity),
		ProviderError:            failure.Error(),
	}))
	if !terminal {
		return nil
	}
	recovery, err := s.recoverySnapshot(sessionID)
	if err != nil {
		return err
	}
	if err := s.publishSnapshot(sessionID, recovery); err != nil {
		return fmt.Errorf("publish terminal runtime activity recovery: %w", err)
	}
	return nil
}

func (s stepLifecycleSink) recoverySnapshot(sessionID string) (runtimeactivity.ResponseSnapshot, error) {
	if provider, ok := s.publisher.(RuntimeReadModelSnapshotProvider); ok {
		return provider.RuntimeReadModelSnapshot(context.Background(), sessionID, nil)
	}
	return terminalRecoverySnapshot(sessionID, s.registrySnapshot()), nil
}

func terminalRecoverySnapshot(sessionID string, registry runtimeactivity.RegistrySnapshot) runtimeactivity.ResponseSnapshot {
	version := runtimeactivity.NextReadModelVersion(sessionID)
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityUnavailable, clientui.RuntimeActivityOptions{DiagnosticRecovery: true})
	if registry.Registered {
		state := clientui.RuntimeActivityRegisteredIdle
		if registry.Closing {
			state = clientui.RuntimeActivityClosing
		} else if registry.Draining {
			state = clientui.RuntimeActivityDraining
		}
		activity = clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{
			QueueAccepting:     registry.QueueAccepting,
			DiagnosticRecovery: true,
		})
		if state != clientui.RuntimeActivityRegisteredIdle {
			activity = clientui.MustRuntimeActivity(state, clientui.RuntimeActivityOptions{DiagnosticRecovery: true})
		}
	}
	return runtimeactivity.ResponseSnapshot{
		Version:             version,
		Activity:            activity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
	}
}
