set working-directory := ".."

deps:
    just _node
    bash scripts/update-deps.sh
