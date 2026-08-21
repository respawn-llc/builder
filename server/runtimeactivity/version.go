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
	return mustReadModelVersion(processEpoch, s.generation, s.sequence.Add(1))
}

func NextReadModelVersion(sessionID string) clientui.ReadModelVersion {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		id = "unknown"
	}
	return mustReadModelVersion(
		processEpoch+"-"+id,
		1,
		fallbackSequence.Add(1),
	)
}

func BuildFeedSnapshot(
	version clientui.ReadModelVersion,
	resolver ResolverSnapshot,
) (clientui.RuntimeReadModelUpdate, error) {
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

func mustReadModelVersion(epoch string, generation uint64, sequence uint64) clientui.ReadModelVersion {
	version, err := clientui.NewReadModelVersion(epoch, generation, sequence)
	if err != nil {
		panic(err)
	}
	return version
}
