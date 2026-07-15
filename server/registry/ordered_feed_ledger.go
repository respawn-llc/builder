package registry

type orderedFeedNode[V any] struct {
	value V
	prev  *orderedFeedNode[V]
	next  *orderedFeedNode[V]
}

type orderedFeedList[V any] struct {
	head *orderedFeedNode[V]
	tail *orderedFeedNode[V]
	size int
}

func (l *orderedFeedList[V]) append(value V) *orderedFeedNode[V] {
	node := &orderedFeedNode[V]{value: value, prev: l.tail}
	if l.tail == nil {
		l.head = node
	} else {
		l.tail.next = node
	}
	l.tail = node
	l.size++
	return node
}

func (l *orderedFeedList[V]) remove(node *orderedFeedNode[V]) {
	if node == nil {
		return
	}
	if node.prev == nil {
		l.head = node.next
	} else {
		node.prev.next = node.next
	}
	if node.next == nil {
		l.tail = node.prev
	} else {
		node.next.prev = node.prev
	}
	l.size--
	node.prev = nil
	node.next = nil
}

func (l *orderedFeedList[V]) values() []V {
	values := make([]V, 0, l.size)
	for node := l.head; node != nil; node = node.next {
		values = append(values, node.value)
	}
	return values
}

type orderedFeedLedger[K comparable, V any] struct {
	entries orderedFeedList[V]
	byKey   map[K]*orderedFeedNode[V]
}

func (l *orderedFeedLedger[K, V]) len() int {
	return l.entries.size
}

func (l *orderedFeedLedger[K, V]) get(key K) (V, bool) {
	node := l.byKey[key]
	if node == nil {
		var zero V
		return zero, false
	}
	return node.value, true
}

func (l *orderedFeedLedger[K, V]) upsert(key K, value V) {
	if l.byKey == nil {
		l.byKey = make(map[K]*orderedFeedNode[V])
	}
	if node := l.byKey[key]; node != nil {
		node.value = value
		return
	}
	l.byKey[key] = l.entries.append(value)
}

func (l *orderedFeedLedger[K, V]) delete(key K) {
	node := l.byKey[key]
	if node == nil {
		return
	}
	delete(l.byKey, key)
	l.entries.remove(node)
}

func (l *orderedFeedLedger[K, V]) values() []V {
	return l.entries.values()
}
