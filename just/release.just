set working-directory := ".."

# Build and verify all CLI release archives.
cli *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-cli.mjs release {{args}}

# Build or assemble desktop release artifacts.
desktop *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-desktop.mjs release {{args}}

# Update Kent's Homebrew tap.
brew *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-brew.mjs {{args}}

[private]
_cli-build *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-cli.mjs build {{args}}

[private]
_cli-verify-manifest *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-cli.mjs verify-manifest {{args}}

[private]
_cli-verify-linux-static *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-cli.mjs verify-linux-static {{args}}

[private]
_cli-smoke *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-cli.mjs smoke {{args}}

[private]
_windows-installer-smoke *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-cli.mjs windows-installer-smoke {{args}}

[private]
_desktop-build *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-desktop.mjs build {{args}}

[private]
_desktop-assemble *args:
    {{ quote(just_executable()) }} _node-preflight
    node tools/devcmd/release-desktop.mjs assemble {{args}}
