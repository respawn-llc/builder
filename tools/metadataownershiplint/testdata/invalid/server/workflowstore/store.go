package workflowstore

func Leaky() {
	db.QueryContext()
	sqlitegen.NewRaw(db)
	sqlitegen.New(db)
	BeginTransaction()
	queries.WithTx(transaction)
}
