package runtime

func (client *blockingThenQueuedClient) callCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.calls)
}
