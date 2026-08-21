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

type RuntimeReadModelPublisher interface {
	PublishRuntimeReadModelUpdate(sessionID string, update clientui.RuntimeReadModelUpdate)
}

type RuntimeActivityRegistrySnapshotProvider interface {
	RuntimeActivityRegistrySnapshot(sessionID string) runtimeactivity.RegistrySnapshot
}

type RuntimeReadModelFeedSnapshotProvider interface {
	RuntimeReadModelFeedSnapshot(ctx context.Context, sessionID string) (clientui.RuntimeReadModelUpdate, error)
}

func NewStepLifecycleSink(sessionID string, publisher RuntimeReadModelPublisher) runtime.StepLifecycleSink {
	return NewStepLifecycleSinkWithInvariantPolicy(sessionID, publisher, invariant.NewPolicy())
}

func NewStepLifecycleSinkWithInvariantPolicy(sessionID string, publisher RuntimeReadModelPublisher, policy invariant.Policy) runtime.StepLifecycleSink {
	if publisher == nil {
		return nil
	}
	return stepLifecycleSink{sessionID: strings.TrimSpace(sessionID), publisher: publisher, policy: policy}
}

type stepLifecycleSink struct {
	sessionID string
	publisher RuntimeReadModelPublisher
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
	snapshot, err := s.feedSnapshot(sessionID, resolver)
	if err != nil {
		return s.handlePublicationFailure(cause, terminal, resolver, clientui.RuntimeReadModelUpdate{}, err)
	}
	if snapshot.Activity.State == clientui.RuntimeActivityUnavailable {
		snapshot.Activity = clientui.RuntimeActivity{
			State:          clientui.RuntimeActivityRegisteredIdle,
			Reviewer:       clientui.ReviewerActivityInactive,
			QueueAccepting: true,
		}
	}
	s.publisher.PublishRuntimeReadModelUpdate(sessionID, snapshot)
	return nil
}

func (s stepLifecycleSink) feedSnapshot(sessionID string, resolver runtimeactivity.ResolverSnapshot) (clientui.RuntimeReadModelUpdate, error) {
	if provider, ok := s.publisher.(RuntimeReadModelFeedSnapshotProvider); ok {
		return provider.RuntimeReadModelFeedSnapshot(context.Background(), sessionID)
	}
	return runtimeactivity.BuildFeedSnapshot(sessionID, func() (runtimeactivity.ResolverSnapshot, error) {
		return resolver, nil
	})
}

func (s stepLifecycleSink) handlePublicationFailure(cause string, terminal bool, resolver runtimeactivity.ResolverSnapshot, proposed clientui.RuntimeReadModelUpdate, failure error) error {
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
	s.publisher.PublishRuntimeReadModelUpdate(sessionID, recovery)
	return nil
}

func (s stepLifecycleSink) recoverySnapshot(sessionID string) (clientui.RuntimeReadModelUpdate, error) {
	if provider, ok := s.publisher.(RuntimeReadModelFeedSnapshotProvider); ok {
		return provider.RuntimeReadModelFeedSnapshot(context.Background(), sessionID)
	}
	return terminalRecoverySnapshot(sessionID, s.registrySnapshot())
}

func terminalRecoverySnapshot(sessionID string, registry runtimeactivity.RegistrySnapshot) (clientui.RuntimeReadModelUpdate, error) {
	update, err := runtimeactivity.BuildFeedSnapshot(sessionID, func() (runtimeactivity.ResolverSnapshot, error) {
		return runtimeactivity.ResolverSnapshot{Registry: registry}, nil
	})
	if err != nil {
		return clientui.RuntimeReadModelUpdate{}, err
	}
	update.Activity.DiagnosticRecovery = true
	if err := update.Validate(); err != nil {
		return clientui.RuntimeReadModelUpdate{}, err
	}
	return update, nil
}
