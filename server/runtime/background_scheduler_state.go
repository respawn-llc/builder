package runtime

import "fmt"

type backgroundNoticeState interface {
	backgroundNoticeState()
	backgroundNotice() queuedBackgroundNotice
}

type pendingBackgroundNotice struct {
	notice queuedBackgroundNotice
}

func (pendingBackgroundNotice) backgroundNoticeState() {}
func (s pendingBackgroundNotice) backgroundNotice() queuedBackgroundNotice {
	return s.notice
}

type retryDeferredBackgroundNotice struct {
	notice     queuedBackgroundNotice
	generation uint64
}

func (retryDeferredBackgroundNotice) backgroundNoticeState() {}
func (s retryDeferredBackgroundNotice) backgroundNotice() queuedBackgroundNotice {
	return s.notice
}

func newRetryDeferredBackgroundNotice(notice queuedBackgroundNotice, generation uint64) retryDeferredBackgroundNotice {
	if generation == 0 {
		panic("retry-deferred background notice requires generation")
	}
	return retryDeferredBackgroundNotice{notice: notice, generation: generation}
}

type backgroundLifecycleTask interface {
	backgroundLifecycleTask()
	backgroundLifecycleTaskAttempt() uint64
}

type scheduledBackgroundLifecycleTask struct {
	attempt uint64
}

func (scheduledBackgroundLifecycleTask) backgroundLifecycleTask() {}
func (t scheduledBackgroundLifecycleTask) backgroundLifecycleTaskAttempt() uint64 {
	return t.attempt
}

type runningBackgroundLifecycleTask struct {
	attempt uint64
}

func (runningBackgroundLifecycleTask) backgroundLifecycleTask() {}
func (t runningBackgroundLifecycleTask) backgroundLifecycleTaskAttempt() uint64 {
	return t.attempt
}

func nextBackgroundLifecycleAttempt(current uint64) uint64 {
	current++
	if current == 0 {
		panic("background lifecycle task attempt overflow")
	}
	return current
}

func backgroundLifecycleTaskAttempt(task backgroundLifecycleTask) uint64 {
	if task == nil {
		panic("background lifecycle task is absent")
	}
	attempt := task.backgroundLifecycleTaskAttempt()
	if attempt == 0 {
		panic(fmt.Sprintf("background lifecycle task has invalid attempt %d", attempt))
	}
	return attempt
}
