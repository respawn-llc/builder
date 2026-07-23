package sqlitegen

//go:generate sh -c "cd ../../.. && sqlc generate && go run ./server/metadata/querygen annotate-sqlc --input ./server/metadata/sqlitegen/queries.sql.go"
