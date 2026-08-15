set working-directory := ".."

model-request *args:
    bash scripts/dump-model-request.sh {{args}}

metadata-schema *args:
    bash scripts/dump-metadata-schema.sh {{args}}
