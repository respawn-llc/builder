set default-list
set lists
set unstable

mod build 'just/build.just'
mod check 'just/check.just'
mod dev 'just/dev.just'
mod dump 'just/dump.just'
mod install 'just/install.just'
mod lint 'just/lint.just'
mod release 'just/release.just'
mod test 'just/test.just'
mod update 'just/update.just'

# Prepare this checkout for Kent development. Dry-run unless `--apply` is passed.
setup *args: _node-preflight
    node tools/devcmd/setup.mjs --just {{ quote(just_executable()) }} {{ args }}

# Regenerate committed protobuf-derived Go and TypeScript sources.
gen: _node-preflight
    node tools/devcmd/generate.mjs write

[private]
_generate-typescript: _node-preflight
    node tools/devcmd/generate.mjs write --typescript-only

[private]
_generate-go: _node-preflight
    node tools/devcmd/generate.mjs write --kind go

[private]
_check-generation *args: _node-preflight
    node tools/devcmd/generate.mjs check {{ args }}

[private]
_node-preflight node=assert(which("node"), "Node.js 22 or newer is required for this command, but `node` was not found in PATH."):
    {{ node }} tools/devcmd/check-node-version.cjs
