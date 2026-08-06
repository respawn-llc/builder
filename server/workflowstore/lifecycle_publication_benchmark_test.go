package workflowstore

import (
	"context"
	"fmt"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

var lifecyclePublicationBenchmarkRoot lifecycleRoot

func BenchmarkLifecycleRootClone(b *testing.B) {
	for _, cardinality := range []int{1, 100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("active_tasks_%d", cardinality), func(b *testing.B) {
			root, _ := benchmarkLifecycleRoot(b, cardinality)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				lifecyclePublicationBenchmarkRoot = cloneLifecycleRoot(root)
			}
		})
	}
}

func BenchmarkLifecyclePublicationCriticalSection(b *testing.B) {
	for _, cardinality := range []int{1, 100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("active_tasks_%d", cardinality), func(b *testing.B) {
			ctx := context.Background()
			metadataStore := testsetup.OpenStore(b, b.TempDir())
			store, err := New(metadataStore)
			if err != nil {
				b.Fatalf("New Store: %v", err)
			}
			if _, err := store.db.ExecContext(
				ctx,
				"CREATE TABLE lifecycle_publication_benchmark_writes (value INTEGER NOT NULL)",
			); err != nil {
				b.Fatalf("create benchmark write table: %v", err)
			}
			if _, err := store.db.ExecContext(
				ctx,
				"INSERT INTO lifecycle_publication_benchmark_writes (value) VALUES (0)",
			); err != nil {
				b.Fatalf("seed benchmark write table: %v", err)
			}
			publication, err := NewLifecyclePublication(store)
			if err != nil {
				b.Fatalf("NewLifecyclePublication: %v", err)
			}
			root, references := benchmarkLifecycleRoot(b, cardinality)
			publication.root = root
			delta, err := NewTaskLifecycleDelta(
				references[0].TaskID,
				[]LifecycleRunDelta{{
					CurrentNode: references[0],
					Expect:      LifecycleFieldPresent,
					Next:        LifecycleFieldPresent,
				}},
				nil,
			)
			if err != nil {
				b.Fatalf("NewTaskLifecycleDelta: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatalf("BeginTx: %v", err)
				}
				if _, err := tx.ExecContext(
					ctx,
					"UPDATE lifecycle_publication_benchmark_writes SET value = value + 1",
				); err != nil {
					_ = tx.Rollback()
					b.Fatalf("prepare benchmark lifecycle write: %v", err)
				}
				prepared := newPreparedSQLLifecycleMutation(tx)
				b.StartTimer()
				if err := publication.publishPrepared(ctx, prepared, delta); err != nil {
					b.Fatalf("publish prepared lifecycle mutation: %v", err)
				}
			}
		})
	}
}

func benchmarkLifecycleRoot(
	tb testing.TB,
	cardinality int,
) (lifecycleRoot, []workflow.CurrentNodeReference) {
	tb.Helper()
	root := make(lifecycleRoot, cardinality)
	references := make([]workflow.CurrentNodeReference, 0, cardinality)
	for index := range cardinality {
		reference, err := workflow.NewCurrentNodeReference(
			workflow.TaskID(fmt.Sprintf("task-benchmark-%05d", index)),
			workflow.NodeID("node-agent"),
			nil,
		)
		if err != nil {
			tb.Fatalf("NewCurrentNodeReference: %v", err)
		}
		key, err := reference.Key()
		if err != nil {
			tb.Fatalf("Current Node key: %v", err)
		}
		root[reference.TaskID] = lifecycleTaskEntry{
			runs: map[workflow.CurrentNodeReferenceKey]workflow.CurrentNodeReference{
				key: reference,
			},
			exact: make(map[workflow.CurrentNodeReferenceKey]LifecycleExactExecution),
		}
		references = append(references, reference)
	}
	return root, references
}
