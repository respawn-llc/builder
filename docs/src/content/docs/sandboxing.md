---
title: Sandboxing and Security
description: Kent's default trust model, outside-workspace edit prompts, and remote/container server setup.
---

:::warning
Kent is YOLO by default: it does not run tools inside a built-in sandbox.
The agent executes shell commands and file tools in the environment where the Kent server runs.
If that environment can read secrets, reach networks, or modify files, the agent can do the same.
:::

However, Kent's [client-server](../server/) architecture makes it easy to run Kent in a **completely isolated, secure container or VM**.

## Outside-Workspace Edits

By default, native edit tools prompt before modifying files outside the Session's Execution Target Root and the bounded collection of up to 500 most recently attached Workspaces in the Session's current Project. `view_image` uses the same trusted boundary for local image reads. Kent prepares this boundary once for the runtime; native file operations do not query Project metadata for each target.

**This is not sandboxing: the agent can easily bypass this.** It's intended for convenience, hallucination and mismatched working-directory prevention. Edits to another Kent-managed Worktree remain forbidden even when that Worktree belongs to the same Project.

To disable, set config:

```toml
allow_non_cwd_edits = true
```

## Server Boundary

Kent separates frontend clients from the server that owns all of the work. That split makes the server environment the useful security boundary:

- Run `kent serve` on a VM and connect from your laptop.
- Run `kent serve` in Docker and expose only the Kent port.
- Run several isolated servers on different ports for different trust zones.

Consequently, when you create or attach a project against a remote/container server, the workspace path must exist inside that server environment, **not on the client machine**.

## Container Image Shape

A Kent sandbox image should contain:

- A `kent` binary compatible with the client version you use.
- Mandatory server dependencies: shell, `rg` and `git`, for normal operation of the server.
- A `config.toml` file with your setup.
- Optional tools the agent may need: language toolchains, package managers, `rg`, `fd`, `jq`, `patch`, `curl`, `gh`, `wget`, `python` and project-specific CLIs. 
- An (ideally persistent) workspace directory such as `/workspace`.
- A writable Kent persistence root, usually under the sandbox user's home.
- Network policy that matches the task; disable or restrict egress when needed.

Avoid mounting your host home directory, full ~/.kent/, or broad source trees into the sandbox.
Mount only the workspace, caches, and credentials the task needs.

## Example Dockerfile

This is a generic starting point.
Add the language runtimes and project tools your workflows need.

```dockerfile
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/home/kent
ENV SHELL=/bin/bash
ENV KENT_VERSION=

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    fd-find \
    file \
    git \
    jq \
    less \
    netcat-openbsd \
    openssh-client \
    patch \
    procps \
    python3 \
    python3-pip \
    python3-venv \
    ripgrep \
    tar \
    tini \
    unzip \
    xz-utils \
    zip \
  && ln -sf /usr/bin/fdfind /usr/local/bin/fd \
  && useradd --create-home --shell /bin/bash kent \
  && mkdir -p /workspace /home/kent/.kent \
  && chown -R kent:kent /workspace /home/kent

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN curl -fsSL https://kent.sh/install.sh \
  | KENT_PREFIX=/usr/local KENT_VERSION="${KENT_VERSION}" sh

USER kent
WORKDIR /workspace
EXPOSE 53082

ENTRYPOINT ["tini", "--"]
CMD ["kent", "serve"]
```

The image installs the latest release by default.
Build with `docker build --build-arg KENT_VERSION=vX.Y.Z -t kent-sandbox .` if you need to pin one Kent release.
Package-manager cache cleanup is useful for smaller images but omitted here for clarity.

Run the server so it listens inside the container and is reachable from the host:

```bash
docker run --name kent-sandbox --rm -it \
  -p 127.0.0.1:53082:53082 \
  -e KENT_SERVER_HOST=0.0.0.0 \
  -e KENT_SERVER_PORT=53082 \
  -v "$PWD:/workspace" \
  kent-sandbox
```

In another terminal, point the local client at that server:

```bash
KENT_SERVER_HOST=127.0.0.1 KENT_SERVER_PORT=53082 kent project create --path /workspace --name sandbox
KENT_SERVER_HOST=127.0.0.1 KENT_SERVER_PORT=53082 kent
```

The project path is `/workspace` because that is the path visible to the server.
