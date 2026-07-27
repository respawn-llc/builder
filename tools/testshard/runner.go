package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

func runJobs(jobs []testJob, workers int) error {
	orderJobs(jobs)
	runStartedAt := time.Now()
	var outputMu sync.Mutex
	var eventMu sync.Mutex
	return runScheduledJobs(jobs, workers, func(job testJob) error {
		return runJob(job, runStartedAt, &outputMu, &eventMu)
	})
}

func orderJobs(jobs []testJob) {
	sort.SliceStable(jobs, func(left, right int) bool {
		leftPriority := schedulingPriority(jobs[left])
		rightPriority := schedulingPriority(jobs[right])
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		if jobs[left].packageEstimatedWeight != jobs[right].packageEstimatedWeight {
			return jobs[left].packageEstimatedWeight > jobs[right].packageEstimatedWeight
		}
		if jobs[left].estimatedWeight != jobs[right].estimatedWeight {
			return jobs[left].estimatedWeight > jobs[right].estimatedWeight
		}
		if jobs[left].packagePath != jobs[right].packagePath {
			return jobs[left].packagePath < jobs[right].packagePath
		}
		return strings.Join(jobs[left].testNames, "\x00") < strings.Join(jobs[right].testNames, "\x00")
	})
}

func schedulingPriority(job testJob) int {
	if job.packageShardIndex < 0 {
		return job.packageEstimatedWeight
	}
	return job.packageEstimatedWeight / (job.packageShardIndex + 1)
}

func runScheduledJobs(jobs []testJob, workers int, runner func(testJob) error) error {
	if workers <= 0 {
		return errors.New("test shard workers must be positive")
	}
	if runner == nil {
		return errors.New("test shard runner is required")
	}
	pending := append([]testJob(nil), jobs...)
	type completedJob struct {
		job testJob
		err error
	}
	completed := make(chan completedJob, workers)
	active := 0
	var errs []error

	for len(pending) > 0 || active > 0 {
		for active < workers && len(pending) > 0 {
			job := pending[0]
			pending = pending[1:]
			active++
			go func() {
				completed <- completedJob{job: job, err: runner(job)}
			}()
		}
		if active == 0 {
			return errors.New("test shard scheduler found no runnable jobs")
		}
		finished := <-completed
		active--
		if finished.err != nil {
			errs = append(errs, finished.err)
		}
	}
	return errors.Join(errs...)
}

func runJob(job testJob, runStartedAt time.Time, outputMu, eventMu *sync.Mutex) error {
	startedAt := time.Now()
	writeJobEvent(eventMu, jobEvent{
		Event:           "started",
		Package:         job.packagePath,
		ShardID:         jobID(job),
		RootListSHA256:  rootListSHA256(job.testNames),
		TestRoots:       job.testRootCount,
		EstimatedWeight: job.estimatedWeight,
		PackageWeight:   job.packageEstimatedWeight,
		PackageShard:    job.packageShardIndex,
		StartedSecs:     startedAt.Sub(runStartedAt).Seconds(),
	})
	arguments := goTestArguments(job)
	command := exec.Command("go", arguments...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		writeJobEvent(eventMu, completedJobEvent(job, startedAt, err))
		return err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		writeJobEvent(eventMu, completedJobEvent(job, startedAt, err))
		return err
	}
	err = errors.Join(forwardLines(stdout, outputMu), command.Wait())
	writeJobEvent(eventMu, completedJobEvent(job, startedAt, err))
	return err
}

func goTestArguments(job testJob) []string {
	// Every shard selects one package. The scheduler owns process concurrency, so
	// each child must not independently fan out package builds.
	arguments := []string{"test", "-json", "-count=1", "-p", "1", job.packagePath}
	if len(job.testNames) > 0 {
		arguments = append(arguments, "-run", exactTestExpression(job.testNames))
	}
	return arguments
}

func completedJobEvent(job testJob, startedAt time.Time, err error) jobEvent {
	return jobEvent{
		Event:           "completed",
		Package:         job.packagePath,
		ShardID:         jobID(job),
		RootListSHA256:  rootListSHA256(job.testNames),
		TestRoots:       job.testRootCount,
		EstimatedWeight: job.estimatedWeight,
		PackageWeight:   job.packageEstimatedWeight,
		PackageShard:    job.packageShardIndex,
		ElapsedSecs:     time.Since(startedAt).Seconds(),
		Failed:          err != nil,
	}
}

func jobID(job testJob) string {
	rootListHash := rootListSHA256(job.testNames)
	if rootListHash == nil {
		return job.packagePath + ":all"
	}
	return job.packagePath + ":" + (*rootListHash)[:12]
}

func rootListSHA256(names []string) *string {
	if len(names) == 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	hash := fmt.Sprintf("%x", sum)
	return &hash
}

func writeJobEvent(eventMu *sync.Mutex, event jobEvent) {
	encoded, err := json.Marshal(event)
	if err != nil {
		panic(fmt.Sprintf("encode test-shard event: %v", err))
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	_, _ = fmt.Fprintf(os.Stderr, "testshard: %s\n", encoded)
}

func exactTestExpression(names []string) string {
	escaped := make([]string, 0, len(names))
	for _, name := range names {
		escaped = append(escaped, quoteRegexp(name))
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func quoteRegexp(value string) string {
	return strings.NewReplacer(`\`, `\\`, `.`, `\.`, `+`, `\+`, `*`, `\*`, `?`, `\?`, `(`, `\(`, `)`, `\)`, `|`, `\|`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`, `^`, `\^`, `$`, `\$`).Replace(value)
}

func forwardLines(reader io.Reader, outputMu *sync.Mutex) error {
	buffered := bufio.NewReader(reader)
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			outputMu.Lock()
			_, writeErr := os.Stdout.Write(line)
			outputMu.Unlock()
			if writeErr != nil {
				return writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
