set working-directory := ".."

cli *args:
    bash scripts/release-artifacts.sh build {{args}}

desktop *args:
    just _node
    bash scripts/desktop-release.sh build --skip-install {{args}}

brew *args:
    bash scripts/update-brew-tap.sh {{args}}

[private]
_cli-build *args:
    bash scripts/release-artifacts.sh build {{args}}

[private]
_cli-verify-manifest *args:
    bash scripts/release-artifacts.sh verify-manifest {{args}}

[private]
_cli-verify-linux-static *args:
    bash scripts/release-artifacts.sh verify-linux-static {{args}}

[private]
_cli-smoke *args:
    bash scripts/release-artifacts.sh smoke-test {{args}}

[private]
_windows-installer-smoke *args:
    bash scripts/release-artifacts.sh smoke-windows-installer {{args}}

[private]
_desktop-build *args:
    just _node
    bash scripts/desktop-release.sh build {{args}}

[private]
_desktop-assemble *args:
    bash scripts/desktop-release.sh assemble {{args}}
