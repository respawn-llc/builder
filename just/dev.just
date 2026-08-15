set working-directory := ".."

# Start the documentation development server.
docs *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/docs.mjs dev {{args}}

# Start the desktop browser development process.
desktop *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/desktop.mjs dev {{args}}

[private]
_desktop-web *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/desktop.mjs native-dev-web {{args}}
