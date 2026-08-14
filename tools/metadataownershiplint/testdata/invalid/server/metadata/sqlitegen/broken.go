package sqlitegen

func (q *Queries) Broken() {
	q.db.QueryContext()
}
