FROM golang:1.25.0-bookworm AS build

ARG SANDBOX_SNAPSHOT_REF=local
ARG SANDBOX_UV_VERSION=0.11.7

WORKDIR /src

COPY . /src

RUN KENT_SKIP_FRONTEND=1 /src/scripts/build.sh server --output /out/kent

FROM debian:bookworm-slim

ARG SANDBOX_SNAPSHOT_REF=local
ARG SANDBOX_UV_VERSION=0.11.7

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/home/kent
ENV SHELL=/bin/bash
ENV GOPATH=/go
ENV SANDBOX_SEED_ROOT=/opt/kent-sandbox-seed
ENV PATH=/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		bash \
		ca-certificates \
		curl \
		dnsutils \
		fd-find \
		file \
		fzf \
		gh \
		git \
		iproute2 \
		jq \
		less \
		lsof \
		netcat-openbsd \
		openssh-client \
		patch \
		procps \
		python3 \
		python3-pip \
		python3-venv \
		ripgrep \
		rsync \
		sqlite3 \
		strace \
		tini \
		tmux \
		tree \
		trash-cli \
		unzip \
		xz-utils \
		yq \
		zip \
		zsh \
	&& python3 -m pip install --break-system-packages --no-cache-dir "uv==${SANDBOX_UV_VERSION}" \
	&& useradd --create-home --shell /bin/bash kent \
	&& mkdir -p /go/bin /go/pkg /opt/kent-sandbox-seed /workspace \
	&& ln -sf /usr/bin/fdfind /usr/local/bin/fd \
	&& ln -sf /usr/bin/pip3 /usr/local/bin/pip \
	&& ln -sf /usr/bin/python3 /usr/local/bin/python \
	&& printf 'export GOPATH=/go\nexport PATH=/go/bin:/usr/local/go/bin:$PATH\n' >/etc/profile.d/go-path.sh \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /usr/local/go /usr/local/go
COPY --from=build /out/kent /usr/local/bin/kent
COPY --from=build /src /opt/kent-sandbox-seed
COPY scripts/sandbox/kent-sandbox-entrypoint.sh /usr/local/bin/kent-sandbox-entrypoint

WORKDIR /opt/kent-sandbox-seed

RUN chmod +x /usr/local/bin/kent /usr/local/bin/kent-sandbox-entrypoint \
	&& git -C /opt/kent-sandbox-seed init -q \
	&& git -C /opt/kent-sandbox-seed config user.name "Kent Sandbox" \
	&& git -C /opt/kent-sandbox-seed config user.email "kent-sandbox@local" \
	&& git -C /opt/kent-sandbox-seed add -A \
	&& git -C /opt/kent-sandbox-seed commit -qm "chore: sandbox seed ${SANDBOX_SNAPSHOT_REF}" \
	&& chown -R kent:kent /go /home/kent /opt/kent-sandbox-seed /workspace

ENTRYPOINT ["tini", "--", "/usr/local/bin/kent-sandbox-entrypoint"]
