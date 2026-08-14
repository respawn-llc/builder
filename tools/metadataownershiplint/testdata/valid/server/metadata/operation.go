package metadata

func RunOperation() {
	sqlitegen.NewRaw(db)
}

func BeginTransaction() {
	db.BeginTx()
}
