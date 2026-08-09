# KENT-438 lifecycle publication benchmark evidence

Captured on August 6, 2026.

## Environment

- OS: macOS 27.0 (build 26A5388g)
- Architecture: darwin/arm64
- CPU: Apple M5 Max
- Go: go1.26.5
- Package: `core/server/workflowstore`

## Command

```text
go test ./server/workflowstore -run '^$' -bench 'BenchmarkLifecycle(RootClone|PublicationCriticalSection)$' -benchtime=1s -count=1 -benchmem
```

`BenchmarkLifecycleRootClone` measures only cloning the immutable plain-map
root. `BenchmarkLifecyclePublicationCriticalSection` prepares an actual SQLite
write transaction before timing, then measures publication lock acquisition,
latest-root cloning, typed delta application, SQLite WAL commit, and root swap.
The benchmark does not define an SLA.

## Exact results

```text
goos: darwin
goarch: arm64
pkg: core/server/workflowstore
cpu: Apple M5 Max
BenchmarkLifecycleRootClone/active_tasks_1-18      	 4377674	       307.6 ns/op	     912 B/op	       5 allocs/op
BenchmarkLifecycleRootClone/active_tasks_100-18    	   53491	     23627 ns/op	   62552 B/op	     304 allocs/op
BenchmarkLifecycleRootClone/active_tasks_1000-18   	    5061	    230610 ns/op	  658053 B/op	    3006 allocs/op
BenchmarkLifecycleRootClone/active_tasks_10000-18  	     472	   2522845 ns/op	 6416051 B/op	   30034 allocs/op
BenchmarkLifecyclePublicationCriticalSection/active_tasks_1-18         	   96052	     12891 ns/op	    1521 B/op	       9 allocs/op
BenchmarkLifecyclePublicationCriticalSection/active_tasks_100-18       	   32728	     36598 ns/op	   63162 B/op	     308 allocs/op
BenchmarkLifecyclePublicationCriticalSection/active_tasks_1000-18      	    4962	    241803 ns/op	  658661 B/op	    3010 allocs/op
BenchmarkLifecyclePublicationCriticalSection/active_tasks_10000-18     	     466	   2671225 ns/op	 6416658 B/op	   30038 allocs/op
PASS
ok  	core/server/workflowstore	21.151s
```

## Decision

The measured critical section was 12.89 µs at 1 active/transitioning Task,
36.60 µs at 100, 241.80 µs at 1,000, and 2.67 ms at 10,000 on this machine.
The plain immutable map is adequate evidence for the approved fast-read
behavior at the representative cardinalities measured here. No persistent
collection or latency guarantee was introduced.
