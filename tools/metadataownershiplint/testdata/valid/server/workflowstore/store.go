package workflowstore

func Owned() {
	transaction := BeginTransaction()
	defer transaction.Settle()
	transaction.Queries()
}
