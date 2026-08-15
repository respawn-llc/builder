set working-directory := ".."

# Capture a diagnostic model request for a persisted session.
model-request *args:
    go run ./cmd/dumpmodelrequest {{args}}

# Print the latest effective metadata SQLite schema.
metadata-schema *args:
    go run ./cmd/dumpmetadataschema {{args}}
