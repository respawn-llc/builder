package session

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMigrationCorrelationSorterResolvesRepeatedOutOfOrderAndUnmatchedQueries(t *testing.T) {
	sorter, ledger := newMigrationCorrelationSorterForTest(t, context.Background(), osMigrationSpoolStorage{})
	mustAddCorrelationCall(t, sorter, "duplicate", 2, 0, false, "first")
	mustAddCorrelationCall(t, sorter, "duplicate", 4, 0, true, "second")
	mustAddCorrelationQuery(t, sorter, "duplicate", 1, 0, "early")
	mustAddCorrelationQuery(t, sorter, "duplicate", 5, 0, "")
	mustAddCorrelationQuery(t, sorter, "duplicate", 6, 0, "query-name")
	mustAddCorrelationQuery(t, sorter, "missing", 8, 0, "missing-name")
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatalf("finish sorter: %v", err)
	}
	got := readMigrationCorrelationResolutions(t, stream)
	want := []migrationCorrelationResolution{
		{Sequence: 1, Name: "early"},
		{Sequence: 5, Custom: true, Name: "second"},
		{Sequence: 6, Custom: true, Name: "query-name"},
		{Sequence: 8, Name: "missing-name"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolutions = %#v, want %#v", got, want)
	}
	assertMigrationCorrelationClean(t, sorter, ledger)
}

func TestMigrationCorrelationSorterUsesSequentialSourcesAndExternalRuns(t *testing.T) {
	sorter, ledger := newMigrationCorrelationSorterForTest(t, context.Background(), osMigrationSpoolStorage{})
	hugeID := strings.Repeat("x", 65_537)
	hugeName := strings.Repeat("n", 65_537)
	for index := 0; index < 130; index++ {
		id := append([]byte(nil), hugeID...)
		id[len(id)-1] = byte('a' + index%20)
		sequence := int64(index*2 + 1)
		if err := sorter.AddCall(migrationCorrelationCallDefinition{
			NormalizedCallID: id, Sequence: sequence, Custom: index%2 == 0, Name: "call",
		}); err != nil {
			t.Fatalf("add call %d: %v", index, err)
		}
		if err := sorter.AddQuery(migrationCorrelationCompletionQuery{
			NormalizedCallID: id, Sequence: sequence + 1, Name: hugeName,
		}); err != nil {
			t.Fatalf("add query %d: %v", index, err)
		}
	}
	if sorter.CreatedArtifactCount() != 4 {
		t.Fatalf("tuple ingestion created artifacts: %d, want four source files", sorter.CreatedArtifactCount())
	}
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatalf("finish external sorter: %v", err)
	}
	count := 0
	for {
		resolution, found, err := stream.Next()
		if err != nil {
			t.Fatalf("next resolution %d: %v", count, err)
		}
		if !found {
			break
		}
		if resolution.Sequence != int64(count*2+2) || resolution.Name != hugeName {
			t.Fatalf("resolution %d = %#v", count, resolution)
		}
		count++
	}
	if count != 130 {
		t.Fatalf("resolution count = %d, want 130", count)
	}
	stats := ledger.snapshot()
	if stats.MaxLiveInlineBytes > migrationCorrelationRunBudgetBytes ||
		stats.MaxOpenSpoolFiles > migrationMaxOpenSpoolFiles ||
		stats.MaxEncoderMergeBytes > migrationEncoderMergeBudgetBytes ||
		stats.PeakSpoolBytes <= migrationCorrelationRunBudgetBytes {
		t.Fatalf("bounded resource stats = %+v", stats)
	}
	assertMigrationCorrelationClean(t, sorter, ledger)
}

func TestMigrationCorrelationSorterMergesMoreThanFanInRuns(t *testing.T) {
	sorter, ledger := newMigrationCorrelationSorterForTest(t, context.Background(), osMigrationSpoolStorage{})
	const pairCount = 530
	idSuffix := strings.Repeat("x", 65_536)
	for index := 0; index < pairCount; index++ {
		id := []byte{byte(index%251 + 1)}
		id = append(id, idSuffix...)
		sequence := int64(index*2 + 1)
		if err := sorter.AddCall(migrationCorrelationCallDefinition{
			NormalizedCallID: id,
			Sequence:         sequence,
			Custom:           index%2 == 0,
			Name:             "call",
		}); err != nil {
			t.Fatalf("add multi-pass call %d: %v", index, err)
		}
		if err := sorter.AddQuery(migrationCorrelationCompletionQuery{
			NormalizedCallID: id,
			Sequence:         sequence + 1,
			Name:             "result",
		}); err != nil {
			t.Fatalf("add multi-pass query %d: %v", index, err)
		}
	}
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatalf("finish multi-pass sorter: %v", err)
	}
	count := 0
	for {
		_, found, err := stream.Next()
		if err != nil {
			t.Fatalf("read multi-pass resolution %d: %v", count, err)
		}
		if !found {
			break
		}
		count++
	}
	if count != pairCount {
		t.Fatalf("multi-pass resolution count = %d, want %d", count, pairCount)
	}
	if sorter.CreatedArtifactCount() <= migrationCorrelationMergeFanIn+8 {
		t.Fatalf(
			"multi-pass sort created %d artifacts; fan-in path was not exercised",
			sorter.CreatedArtifactCount(),
		)
	}
	stats := ledger.snapshot()
	if stats.MaxOpenSpoolFiles > migrationMaxOpenSpoolFiles ||
		stats.MaxEncoderMergeBytes > migrationEncoderMergeBudgetBytes ||
		stats.CurrentSpoolBytes != 0 ||
		stats.OpenSpoolFiles != 0 {
		t.Fatalf("multi-pass resource stats = %+v", stats)
	}
	assertMigrationCorrelationClean(t, sorter, ledger)
}

func TestMigrationCorrelationSorterCancelsAndCleansArtifacts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sorter, ledger := newMigrationCorrelationSorterForTest(t, ctx, osMigrationSpoolStorage{})
	mustAddCorrelationCall(t, sorter, "call", 1, 0, true, "patch")
	mustAddCorrelationQuery(t, sorter, "call", 2, 0, "")
	cancel()
	if _, err := sorter.Finish(); !errors.Is(err, context.Canceled) {
		t.Fatalf("finish cancellation error = %v", err)
	}
	assertMigrationCorrelationClean(t, sorter, ledger)
}

func TestMigrationCorrelationSorterCleansCreateAndRemoveFailures(t *testing.T) {
	createFailure := &failingMigrationSpoolStorage{
		delegate: osMigrationSpoolStorage{}, createErr: errors.New("create failed"),
	}
	ledger := newMigrationResourceLedger()
	if _, err := newMigrationCorrelationSorter(context.Background(), t.TempDir(), ledger, createFailure); err == nil {
		t.Fatal("expected creation failure")
	}
	if stats := ledger.snapshot(); stats.OpenSpoolFiles != 0 || stats.CurrentSpoolBytes != 0 {
		t.Fatalf("creation failure leaked resources: %+v", stats)
	}

	removeFailure := &correlationFailingRemoveStorage{
		delegate: osMigrationSpoolStorage{}, removeErr: errors.New("remove failed"),
	}
	sorter, _ := newMigrationCorrelationSorterForTest(t, context.Background(), removeFailure)
	mustAddCorrelationCall(t, sorter, "call", 1, 0, false, "name")
	if err := sorter.Close(); err == nil {
		t.Fatal("expected removal failure")
	}
	if sorter.ArtifactCount() == 0 {
		t.Fatal("failed removal was not retained for retry")
	}
	removeFailure.removeErr = nil
	if err := sorter.Close(); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if sorter.ArtifactCount() != 0 {
		t.Fatalf("retry removal retained artifacts: %d", sorter.ArtifactCount())
	}
}

func TestMigrationCorrelationResolutionStreamMissingRunCleansArtifacts(t *testing.T) {
	sorter, ledger := newMigrationCorrelationSorterForTest(t, context.Background(), osMigrationSpoolStorage{})
	mustAddCorrelationCall(t, sorter, "call", 1, 0, true, "patch")
	mustAddCorrelationQuery(t, sorter, "call", 2, 0, "")
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatalf("finish sorter: %v", err)
	}
	if err := os.Remove(sorter.resolutions.data.artifact.path); err != nil {
		t.Fatalf("remove resolution source: %v", err)
	}
	if _, _, err := stream.Next(); err == nil {
		t.Fatal("expected missing final run error")
	}
	assertMigrationCorrelationClean(t, sorter, ledger)
}

func newMigrationCorrelationSorterForTest(
	t *testing.T,
	ctx context.Context,
	storage migrationSpoolStorage,
) (*migrationCorrelationSorter, *migrationResourceLedger) {
	t.Helper()
	ledger := newMigrationResourceLedger()
	sorter, err := newMigrationCorrelationSorter(ctx, t.TempDir(), ledger, storage)
	if err != nil {
		t.Fatalf("new correlation sorter: %v", err)
	}
	return sorter, ledger
}

func mustAddCorrelationCall(t *testing.T, sorter *migrationCorrelationSorter, id string, sequence, ordinal int64, custom bool, name string) {
	t.Helper()
	if err := sorter.AddCall(migrationCorrelationCallDefinition{
		NormalizedCallID: []byte(id), Sequence: sequence, Ordinal: ordinal, Custom: custom, Name: name,
	}); err != nil {
		t.Fatalf("add call: %v", err)
	}
}

func mustAddCorrelationQuery(t *testing.T, sorter *migrationCorrelationSorter, id string, sequence, ordinal int64, name string) {
	t.Helper()
	if err := sorter.AddQuery(migrationCorrelationCompletionQuery{
		NormalizedCallID: []byte(id), Sequence: sequence, Ordinal: ordinal, Name: name,
	}); err != nil {
		t.Fatalf("add query: %v", err)
	}
}

func readMigrationCorrelationResolutions(t *testing.T, stream *migrationCorrelationResolutionStream) []migrationCorrelationResolution {
	t.Helper()
	var result []migrationCorrelationResolution
	for {
		resolution, found, err := stream.Next()
		if err != nil {
			t.Fatalf("next correlation resolution: %v", err)
		}
		if !found {
			return result
		}
		result = append(result, resolution)
	}
}

func assertMigrationCorrelationClean(t *testing.T, sorter *migrationCorrelationSorter, ledger *migrationResourceLedger) {
	t.Helper()
	if sorter.ArtifactCount() != 0 {
		t.Fatalf("live correlation artifacts = %d", sorter.ArtifactCount())
	}
	stats := ledger.snapshot()
	if stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("correlation artifacts leaked: %+v", stats)
	}
}

type correlationFailingRemoveStorage struct {
	delegate  migrationSpoolStorage
	removeErr error
}

func (s *correlationFailingRemoveStorage) Create(dir string) (migrationSpoolWriter, error) {
	return s.delegate.Create(dir)
}

func (s *correlationFailingRemoveStorage) Open(path string) (migrationSpoolReader, error) {
	return s.delegate.Open(path)
}

func (s *correlationFailingRemoveStorage) Remove(string) error {
	return s.removeErr
}
