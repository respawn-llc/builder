#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"

require_option_arg() {
	local option="${1:-}"
	local value="${2:-}"
	if [ -z "$value" ]; then
		die "Missing required argument value: ${option}"
	fi
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/sandbox/sandbox-serve.sh up [options] [-- <kent-serve-args...>]
  scripts/sandbox/sandbox-serve.sh down [options]
  scripts/sandbox/sandbox-serve.sh logs [options] [-- <docker-logs-args...>]
  scripts/sandbox/sandbox-serve.sh shell [options] [-- <command...>]
  scripts/sandbox/sandbox-serve.sh env [options]

Start an isolated Docker sandbox that builds `kent` from the same repo
snapshot copied into the image, clones a sandboxed Kent repo into the
container workspace path, seeds only `config.toml` and `auth.json` into an
isolated home volume on first boot, registers project `kent`, and exposes
the JSON-RPC WebSocket gateway to the host machine.

Seed files are copied only when missing inside the sandbox home volume. Use
`down --reset` to discard sandbox state and re-seed from host files.

Options:
  --name NAME             Container name. Default: kent-sandbox
  --image TAG             Image tag. Default: kent-sandbox:local
  --host-port PORT        Host port to expose. Default: 53082
  --container-port PORT   Container listen port. Default: 53082
  --platform PLATFORM     Docker platform. Default: linux/<host-arch>
  --workspace-root PATH   Absolute container workspace path.
                          Default: /workspace/kent
  --project-name NAME     Server-side project display name. Default: kent
  --config-seed PATH      Host config.toml copied once into sandbox /home/kent/.kent/
                          Default: $HOME/.kent/config.toml
  --auth-seed PATH        Host auth.json copied once into sandbox /home/kent/.kent/
                          Default: $HOME/.kent/auth.json
  --workspace-volume VOL  Named volume for sandbox workspace.
                          Default: <name>-workspace
  --home-volume VOL       Named volume for sandbox home/persistence.
                          Default: <name>-home
  --dry-run               Print planned commands without executing them.
  --reset                 With `down`, remove named volumes too.
  -h, --help              Show this help.

Examples:
  scripts/sandbox/sandbox-serve.sh up
  scripts/sandbox/sandbox-serve.sh up --host-port 53100 --project-name kent -- --model gpt-5.6-sol
  scripts/sandbox/sandbox-serve.sh env --host-port 53100
  scripts/sandbox/sandbox-serve.sh down --reset
USAGE
}

log() {
	printf '==> %s\n' "$*"
}

die() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

run() {
	if [ "$dry_run" = "true" ]; then
		printf '[dry-run]'
		for arg in "$@"; do
			printf ' %q' "$arg"
		done
		printf '\n'
		return 0
	fi
	"$@"
}

docker_ready() {
	docker info >/dev/null 2>&1
}

ensure_docker() {
	if docker_ready; then
		return 0
	fi
	if [ -d /Applications/OrbStack.app ]; then
		run open -j -g /Applications/OrbStack.app
	fi
	if [ "$dry_run" = "true" ]; then
		printf '[dry-run] wait for docker daemon readiness\n'
		return 0
	fi
	local deadline=$((SECONDS + 90))
	until docker_ready; do
		if [ "$SECONDS" -ge "$deadline" ]; then
			die "docker daemon not ready"
		fi
		sleep 1
	done
}

default_platform() {
	case "$(uname -m)" in
	arm64 | aarch64)
		printf 'linux/arm64'
		;;
	x86_64 | amd64)
		printf 'linux/amd64'
		;;
	*)
		die "unsupported host architecture: $(uname -m)"
		;;
	esac
}

snapshot_ref() {
	if git -C "$repo_root" rev-parse --short HEAD >/dev/null 2>&1; then
		git -C "$repo_root" rev-parse --short HEAD
		return 0
	fi
	printf 'local'
}

normalize_absolute_path() {
	local target="${1:-}"
	if [ -z "$target" ]; then
		die "path is required"
	fi
	case "$target" in
	/*) ;;
	*) die "path must be absolute: $target" ;;
	esac
	local parent
	parent="$(dirname -- "$target")"
	if [ -d "$parent" ]; then
		parent="$(cd -- "$parent" && pwd -P)"
		printf '%s/%s' "$parent" "$(basename -- "$target")"
		return 0
	fi
	printf '%s' "$target"
}

canonical_file_path() {
	local target="${1:-}"
	if [ -z "$target" ]; then
		die "file path is required"
	fi
	local dir
	dir="$(cd -- "$(dirname -- "$target")" && pwd -P)"
	printf '%s/%s' "$dir" "$(basename -- "$target")"
}

container_exists() {
	if [ "$dry_run" = "true" ]; then
		return 1
	fi
	docker container inspect "$container_name" >/dev/null 2>&1
}

require_host_port_available() {
	if [ "$dry_run" = "true" ]; then
		return 0
	fi
	if (echo >/dev/tcp/127.0.0.1/"$host_port") >/dev/null 2>&1; then
		die "host port ${host_port} is already in use. Stop the owner or pass --host-port <free-port>."
	fi
}

wait_for_ready() {
	if [ "$dry_run" = "true" ]; then
		printf '[dry-run] poll http://127.0.0.1:%s/healthz until transport is ready\n' "$host_port"
		return 0
	fi
	local deadline=$((SECONDS + 60))
	until curl -fsS "http://127.0.0.1:${host_port}/healthz" >/dev/null 2>&1; do
		if ! docker ps --filter "name=^/${container_name}$" --format '{{.Names}}' | grep -qx "$container_name"; then
			docker logs "$container_name" >&2 || true
			die "sandbox container exited before transport readiness"
		fi
		if [ "$SECONDS" -ge "$deadline" ]; then
			docker logs "$container_name" >&2 || true
			die "sandbox server did not become transport-ready"
		fi
		sleep 1
	done
}

print_env_exports() {
	cat <<EOF
export KENT_SERVER_HOST=127.0.0.1
export KENT_SERVER_PORT=${host_port}
EOF
}

collect_container_env() {
	container_env=()
	for key in OPENAI_API_KEY KENT_OAUTH_CLIENT_ID KENT_PROVIDER_OVERRIDE KENT_OPENAI_BASE_URL; do
		value="${!key:-}"
		if [ -n "$value" ]; then
			container_env+=("-e" "${key}")
		fi
	done
}

collect_seed_mounts() {
	seed_mounts=()
	if [ -n "$config_seed" ] && [ -f "$config_seed" ]; then
		config_seed="$(canonical_file_path "$config_seed")"
		seed_mounts+=("-v" "${config_seed}:/opt/kent-sandbox-seeds/config.toml:ro" "-e" "SANDBOX_CONFIG_SEED_PATH=/opt/kent-sandbox-seeds/config.toml")
	fi
	if [ -n "$auth_seed" ] && [ -f "$auth_seed" ]; then
		auth_seed="$(canonical_file_path "$auth_seed")"
		seed_mounts+=("-v" "${auth_seed}:/opt/kent-sandbox-seeds/auth.json:ro" "-e" "SANDBOX_AUTH_SEED_PATH=/opt/kent-sandbox-seeds/auth.json")
	fi
}

build_image() {
	log "build sandbox image ${image_tag}"
	run docker build \
		--platform "$platform" \
		-f "$repo_root/scripts/sandbox/kent-sandbox.Dockerfile" \
		--build-arg "SANDBOX_SNAPSHOT_REF=$(snapshot_ref)" \
		-t "$image_tag" \
		"$repo_root"
}

run_up() {
	workspace_root="$(normalize_absolute_path "$workspace_root")"
	workspace_volume="${workspace_volume:-${container_name}-workspace}"
	home_volume="${home_volume:-${container_name}-home}"
	serve_args=("${passthrough_args[@]}")
	ensure_docker
	if container_exists; then
		log "replace existing container ${container_name}"
		run docker container rm -f "$container_name"
	fi
	require_host_port_available
	build_image
	collect_container_env
	collect_seed_mounts
	run docker volume create "$workspace_volume"
	run docker volume create "$home_volume"
	log "start sandbox container ${container_name}"
	run docker run -d \
		--name "$container_name" \
		--platform "$platform" \
		-p "${host_port}:${container_port}" \
		-e "KENT_SERVER_HOST=0.0.0.0" \
		-e "KENT_SERVER_PORT=${container_port}" \
		-e "SANDBOX_WORKSPACE_ROOT=${workspace_root}" \
		-e "SANDBOX_SEED_ROOT=/opt/kent-sandbox-seed" \
		-e "SANDBOX_PROJECT_NAME=${project_name}" \
		-e "HOME=/home/kent" \
		"${container_env[@]}" \
		"${seed_mounts[@]}" \
		-v "${workspace_volume}:${workspace_root}" \
		-v "${home_volume}:/home/kent" \
		"$image_tag" \
		"${serve_args[@]}"
	wait_for_ready
	cat <<EOF
Sandbox ready.

Container:  ${container_name}
Image:      ${image_tag}
Workspace:  ${workspace_root}
Gateway:    ws://127.0.0.1:${host_port}/rpc
Health:     http://127.0.0.1:${host_port}/healthz
Readiness:  http://127.0.0.1:${host_port}/readyz

Host client env:
$(print_env_exports)

Useful commands:
  scripts/sandbox/sandbox-serve.sh logs --name ${container_name}
  scripts/sandbox/sandbox-serve.sh shell --name ${container_name}
  scripts/sandbox/sandbox-serve.sh down --name ${container_name}
EOF
}

run_down() {
	ensure_docker
	workspace_volume="${workspace_volume:-${container_name}-workspace}"
	home_volume="${home_volume:-${container_name}-home}"
	if container_exists; then
		log "stop sandbox container ${container_name}"
		run docker container rm -f "$container_name"
	else
		log "container ${container_name} not running"
	fi
	if [ "$reset" = "true" ]; then
		log "remove sandbox volumes"
		run docker volume rm -f "$workspace_volume" "$home_volume"
	fi
}

run_logs() {
	ensure_docker
	run docker logs -f "${passthrough_args[@]}" "$container_name"
}

run_shell() {
	ensure_docker
	if [ ${#passthrough_args[@]} -eq 0 ]; then
		passthrough_args=(/bin/bash)
	fi
	if [ "$dry_run" = "true" ]; then
		printf '[dry-run]'
		printf ' %q' docker exec -it "$container_name" "${passthrough_args[@]}"
		printf '\n'
		return 0
	fi
	docker exec -it "$container_name" "${passthrough_args[@]}"
}

command_name="${1:-}"

if [ "$command_name" = "-h" ] || [ "$command_name" = "--help" ]; then
	usage
	exit 0
fi

if [ -z "$command_name" ]; then
	usage >&2
	exit 1
fi

shift || true

container_name="kent-sandbox"
image_tag="kent-sandbox:local"
host_port="53082"
container_port="53082"
platform="$(default_platform)"
workspace_root="/workspace/kent"
project_name="kent"
config_seed="${HOME:-}/.kent/config.toml"
auth_seed="${HOME:-}/.kent/auth.json"
workspace_volume=""
home_volume=""
dry_run="false"
reset="false"
serve_args=()
passthrough_args=()
seed_mounts=()

while [ $# -gt 0 ]; do
	case "$1" in
	--name)
		require_option_arg "$1" "${2:-}"
		container_name="${2:-}"
		shift 2
		;;
	--image)
		require_option_arg "$1" "${2:-}"
		image_tag="${2:-}"
		shift 2
		;;
	--host-port)
		require_option_arg "$1" "${2:-}"
		host_port="${2:-}"
		shift 2
		;;
	--container-port)
		require_option_arg "$1" "${2:-}"
		container_port="${2:-}"
		shift 2
		;;
	--platform)
		require_option_arg "$1" "${2:-}"
		platform="${2:-}"
		shift 2
		;;
	--workspace-root)
		require_option_arg "$1" "${2:-}"
		workspace_root="${2:-}"
		shift 2
		;;
	--project-name)
		require_option_arg "$1" "${2:-}"
		project_name="${2:-}"
		shift 2
		;;
	--config-seed)
		require_option_arg "$1" "${2:-}"
		config_seed="${2:-}"
		shift 2
		;;
	--auth-seed)
		require_option_arg "$1" "${2:-}"
		auth_seed="${2:-}"
		shift 2
		;;
	--workspace-volume)
		require_option_arg "$1" "${2:-}"
		workspace_volume="${2:-}"
		shift 2
		;;
	--home-volume)
		require_option_arg "$1" "${2:-}"
		home_volume="${2:-}"
		shift 2
		;;
	--dry-run)
		dry_run="true"
		shift
		;;
	--reset)
		reset="true"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	--)
		shift
		passthrough_args=("$@")
		break
		;;
	*)
		die "Unknown argument: $1"
		;;
	esac
done

if [ -z "$container_name" ]; then
	die "--name must not be empty"
fi
if [ -z "$image_tag" ]; then
	die "--image must not be empty"
fi
if [ -z "$host_port" ] || [ -z "$container_port" ]; then
	die "ports must not be empty"
fi

case "$command_name" in
up)
	run_up
	;;
down)
	if [ ${#passthrough_args[@]} -gt 0 ]; then
		die "down does not accept passthrough args"
	fi
	run_down
	;;
logs)
	run_logs
	;;
shell)
	run_shell
	;;
env)
	if [ ${#passthrough_args[@]} -gt 0 ]; then
		die "env does not accept passthrough args"
	fi
	print_env_exports
	;;
*)
	usage >&2
	die "Unknown command: ${command_name}"
	;;
esac
