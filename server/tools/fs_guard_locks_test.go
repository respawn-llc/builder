package tools

import (
	"testing"
	"time"
)

func TestLockPathsDoesNotBlockUnrelatedPaths(t *testing.T) {
	unlockA := LockFSGuardPaths([]string{"a"})

	unrelated := make(chan struct{})
	go func() {
		unlockB := LockFSGuardPaths([]string{"b"})
		unlockB()
		close(unrelated)
	}()
	select {
	case <-unrelated:
	case <-time.After(time.Second):
		t.Fatal("unrelated path lock blocked")
	}

	same := make(chan struct{})
	go func() {
		unlockA2 := LockFSGuardPaths([]string{"a"})
		unlockA2()
		close(same)
	}()
	select {
	case <-same:
		t.Fatal("same path lock did not block")
	case <-time.After(50 * time.Millisecond):
	}
	unlockA()
	select {
	case <-same:
	case <-time.After(time.Second):
		t.Fatal("same path lock did not unblock")
	}
}

func TestLockPathsNormalizesEquivalentPaths(t *testing.T) {
	unlockA := LockFSGuardPaths([]string{"a/../x"})
	same := make(chan struct{})
	go func() {
		unlockB := LockFSGuardPaths([]string{"./x"})
		unlockB()
		close(same)
	}()
	select {
	case <-same:
		t.Fatal("equivalent path lock did not block")
	case <-time.After(50 * time.Millisecond):
	}
	unlockA()
	select {
	case <-same:
	case <-time.After(time.Second):
		t.Fatal("equivalent path lock did not unblock")
	}
}

func TestLockPathEmptyKeyIsNoop(t *testing.T) {
	unlock := LockFSGuardPath(" \t")
	done := make(chan struct{})
	go func() {
		unlockB := LockFSGuardPath("")
		unlockB()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("empty lock path blocked")
	}
	unlock()
}
