package workflowview

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/sessioncontract"
)

const (
	sqlitePoolBenchmarkTaskReaders     = 4
	sqlitePoolBenchmarkQuestionAnswers = 2
	sqlitePoolBenchmarkSessionWriters  = 2
)

type sqlitePoolBenchmarkFixture struct {
	detail    *TaskDetail
	metadata  *metadata.Store
	workflow  *workflowstore.Store
	taskID    workflow.TaskID
	runID     workflow.RunID
	askID     string
	snapshots []session.PersistedStoreSnapshot
}

func BenchmarkSQLitePoolMixedWorkflowLoad(b *testing.B) {
	for _, connectionBound := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("connections=%d", connectionBound), func(b *testing.B) {
			fixture := newSQLitePoolBenchmarkFixture(b)
			fixture.metadata.DB().SetMaxOpenConns(connectionBound)
			fixture.metadata.DB().SetMaxIdleConns(connectionBound)
			fixture.run(b)
		})
	}
}

func newSQLitePoolBenchmarkFixture(b *testing.B) sqlitePoolBenchmarkFixture {
	b.Helper()
	ctx := context.Background()
	metadataStore, workflowStore, binding := newWorkflowViewTestStore(b)
	workflowID := createWorkflowViewValidWorkflow(b, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		b.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "SQLite pool benchmark",
		Body:      "Exercise concurrent task reads, question answers, and session persistence.",
	})
	if err != nil {
		b.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		b.Fatalf("StartTask: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		b.Fatalf("ClaimRun: %v", err)
	}
	snapshots := make([]session.PersistedStoreSnapshot, 0, sqlitePoolBenchmarkSessionWriters)
	for range sqlitePoolBenchmarkSessionWriters {
		sessionStore, createErr := session.Create(
			filepath.Join(metadataStore.PersistenceRoot(), "projects", binding.ProjectID, "sessions"),
			binding.WorkspaceName,
			binding.CanonicalRoot,
			sessioncontract.SessionCategoryMain,
			metadataStore.AuthoritativeSessionStoreOptions()...,
		)
		if createErr != nil {
			b.Fatalf("session.Create: %v", createErr)
		}
		if durableErr := sessionStore.EnsureDurable(); durableErr != nil {
			b.Fatalf("EnsureDurable: %v", durableErr)
		}
		snapshots = append(snapshots, session.PersistedStoreSnapshot{
			SessionDir: sessionStore.Dir(),
			Meta:       sessionStore.Meta(),
		})
	}
	if err := workflowStore.AttachRunSession(ctx, started.RunID, claimed.Generation, snapshots[0].Meta.SessionID); err != nil {
		b.Fatalf("AttachRunSession: %v", err)
	}
	const askID = "sqlite-pool-benchmark-question"
	if err := workflowStore.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, askID); err != nil {
		b.Fatalf("SetRunWaitingAsk: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	b.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			b.Errorf("close session authority: %v", err)
		}
	})
	detail, err := NewTaskDetail(metadataStore, NewTaskProjector(), authority)
	if err != nil {
		b.Fatalf("NewTaskDetail: %v", err)
	}
	return sqlitePoolBenchmarkFixture{
		detail:    detail,
		metadata:  metadataStore,
		workflow:  workflowStore,
		taskID:    task.ID,
		runID:     started.RunID,
		askID:     askID,
		snapshots: snapshots,
	}
}

func (f sqlitePoolBenchmarkFixture) run(b *testing.B) {
	b.Helper()
	var taskReadNanos atomic.Int64
	var questionAnswerNanos atomic.Int64
	var sessionWriteNanos atomic.Int64
	before := f.metadata.DB().Stats()
	b.ResetTimer()
	for range b.N {
		start := make(chan struct{})
		errs := make(chan error, sqlitePoolBenchmarkTaskReaders+sqlitePoolBenchmarkQuestionAnswers+sqlitePoolBenchmarkSessionWriters)
		var workers sync.WaitGroup
		run := func(operation func() error, elapsed *atomic.Int64) {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				startedAt := time.Now()
				errs <- operation()
				elapsed.Add(time.Since(startedAt).Nanoseconds())
			}()
		}
		for range sqlitePoolBenchmarkTaskReaders {
			run(func() error {
				_, err := f.detail.GetTask(context.Background(), string(f.taskID))
				return err
			}, &taskReadNanos)
		}
		for range sqlitePoolBenchmarkQuestionAnswers {
			run(func() error {
				_, err := f.workflow.ResolveTaskWaitingAsk(context.Background(), f.taskID, f.runID, f.askID)
				return err
			}, &questionAnswerNanos)
		}
		for i := range sqlitePoolBenchmarkSessionWriters {
			snapshot := f.snapshots[i]
			run(func() error {
				return f.metadata.ImportSessionSnapshot(context.Background(), snapshot)
			}, &sessionWriteNanos)
		}
		close(start)
		workers.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				b.Fatalf("mixed SQLite workload: %v", err)
			}
		}
	}
	b.StopTimer()
	after := f.metadata.DB().Stats()
	rounds := float64(b.N)
	b.ReportMetric(float64(taskReadNanos.Load())/(rounds*sqlitePoolBenchmarkTaskReaders), "ns/task-read")
	b.ReportMetric(float64(questionAnswerNanos.Load())/(rounds*sqlitePoolBenchmarkQuestionAnswers), "ns/question-answer-db")
	b.ReportMetric(float64(sessionWriteNanos.Load())/(rounds*sqlitePoolBenchmarkSessionWriters), "ns/session-write")
	b.ReportMetric(float64(after.WaitCount-before.WaitCount)/rounds, "pool-waits/round")
	b.ReportMetric(float64((after.WaitDuration-before.WaitDuration).Nanoseconds())/rounds, "pool-wait-ns/round")
}
