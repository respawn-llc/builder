package sqlitegen

func (q *Queries) Owned() {
	q.beforeOperation()
	q.completeOperation()
}
