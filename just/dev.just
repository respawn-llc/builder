set working-directory := ".."

desktop *args:
    just _node
    just _gen-typescript
    pnpm --dir apps/desktop exec vite --config tooling/vite.config.ts --host 127.0.0.1 --open / {{args}}

docs *args:
    just _node
    pnpm --dir docs exec astro dev {{args}}

[private]
_desktop-web *args:
    just _node
    just _gen-typescript
    pnpm --dir apps/desktop exec vite --config tooling/vite.config.ts --host 127.0.0.1 {{args}}
