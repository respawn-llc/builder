set working-directory := ".."

# Build and install the desktop application.
desktop *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/desktop.mjs install {{args}}

[private]
_apps-dependencies:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/dependencies.mjs install apps

[private]
_docs-dependencies:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/dependencies.mjs install docs

[private]
_dependencies:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/dependencies.mjs install all
