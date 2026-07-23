package sqlitegen

//go:generate sh -c "cd ../../.. && sqlc generate && go run ./server/metadata/sqlcdiagnosticgen --input ./server/metadata/sqlitegen/queries.sql.go"
