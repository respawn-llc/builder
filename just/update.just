set working-directory := ".."

# Update every active dependency ecosystem.
deps *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/dependencies.mjs update {{args}}
