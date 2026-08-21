package runtimeactivity

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"core/shared/clientui"
)

var (
	processEpoch     = "process-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	fallbackSequence atomic.Uint64
)

type SnapshotBuilder func() (ResolverSnapshot, error)

type ResponseSnapshot struct {
	Version  clientui.ReadModelVersion
	Activity clientui.RuntimeActivity
}

type VersionSource struct {
	generation uint64
	sequence   atomic.Uint64
}

func NewVersionSource(generation uint64) *VersionSource {
	if generation == 0 {
		panic("read model version generation is required")
	}
	return &VersionSource{generation: generation}
}

func (s *VersionSource) Next() clientui.ReadModelVersion {
	if s == nil {
		return NextReadModelVersion("unknown")
	}
	return mustReadModelVersion(processEpoch, s.generation, s.sequence.Add(1))
}

func (s *VersionSource) FeedSnapshot(build SnapshotBuilder) (clientui.RuntimeReadModelUpdate, error) {
	if s == nil {
		return BuildFeedSnapshot("unknown", build)
	}
	return buildFeedSnapshot(s.Next(), build)
}

func NextReadModelVersion(sessionID string) clientui.ReadModelVersion {
	return mustReadModelVersion(
		processEpoch+"-"+versionSessionID(sessionID),
		1,
		fallbackSequence.Add(1),
	)
}

func BuildFeedSnapshot(sessionID string, build SnapshotBuilder) (clientui.RuntimeReadModelUpdate, error) {
	return buildFeedSnapshot(NextReadModelVersion(sessionID), build)
}

func buildFeedSnapshot(version clientui.ReadModelVersion, build SnapshotBuilder) (clientui.RuntimeReadModelUpdate, error) {
	if build == nil {
		return clientui.RuntimeReadModelUpdate{}, nil
	}
	resolver, err := build()
	if err != nil {
		return clientui.RuntimeReadModelUpdate{}, err
	}
	activity, err := resolveRuntimeFeedActivity(resolver)
	if err != nil {
		return clientui.RuntimeReadModelUpdate{}, err
	}
	update := clientui.RuntimeReadModelUpdate{Version: version, Activity: activity}
	if err := update.Validate(); err != nil {
		return clientui.RuntimeReadModelUpdate{}, fmt.Errorf("validate runtime feed read-model update: %w", err)
	}
	return update, nil
}

func versionSessionID(sessionID string) string {
	if id := strings.TrimSpace(sessionID); id != "" {
		return id
	}
	return "unknown"
}

func mustReadModelVersion(epoch string, generation uint64, sequence uint64) clientui.ReadModelVersion {
	version, err := clientui.NewReadModelVersion(epoch, generation, sequence)
	if err != nil {
		panic(err)
	}
	return version
}
