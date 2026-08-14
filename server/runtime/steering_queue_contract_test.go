package runtime

import "testing"

func TestSteeringQueueFIFOAndDrainingHeadContract(t *testing.T) {
	queue := newSteeringQueue()
	first := newUserShellQueueEntry("first", nil)
	second := newUserShellQueueEntry("second", nil)
	third := newUserShellQueueEntry("third", nil)
	_, _ = queue.append(first)
	_, _ = queue.append(second)
	head, ok := queue.beginNext(true)
	if !ok || head != first || !queue.pendingWork() || queue.finishDrain(nil) {
		t.Fatal("first dequeued head did not retain FIFO Draining ownership")
	}
	_, _ = queue.append(third)
	_ = queue.finishCurrent(first)
	for _, want := range []*steeringQueueEntry{second, third} {
		head, ok = queue.beginNext(true)
		if !ok || head != want {
			t.Fatal("arrival during drain changed accepted FIFO order")
		}
		_ = queue.finishCurrent(want)
	}
	if !queue.finishDrain(nil) || queue.pendingWork() {
		t.Fatal("queue did not become Idle after finalizing the accepted FIFO")
	}
}
