#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat <<'USAGE'
Usage: ./scripts/import_gh.sh <ref> [<ref> ...]

Imports GitHub issues into the Kent Task workflow linked as the default for the
current working directory. Priority, effort, Bug/Feature issue type, and
repository labels are assigned to each task; missing Project labels are created.

Each <ref> is one of:
  - an issue number             e.g. 418
  - an issue URL                e.g. https://github.com/owner/repo/issues/418
  - an inclusive number range   e.g. 423..437
  - a comma-separated list      e.g. 423,425,430

Refs may be combined, e.g.:
  ./scripts/import_gh.sh 418 423..437 https://github.com/owner/repo/issues/440
USAGE
}

fail() {
	echo "$1" >&2
	exit 1
}

require_command() {
	if ! command -v "$1" >/dev/null 2>&1; then
		fail "$1 is required."
	fi
}

load_project_label_catalog() {
	"$kent_bin" task label list --project . --json >"$label_catalog_file"
	if ! jq -e '.catalog.labels | type == "array"' "$label_catalog_file" >/dev/null; then
		fail "Kent returned an invalid Project label catalog."
	fi
}

ensure_project_label() {
	local requested_name="$1"
	local existing_name
	existing_name="$(
		jq -r --arg name "$requested_name" '
			(
				first(.catalog.labels[] | select(.name == $name) | .name)
				//
				first(
					.catalog.labels[]
					| select((.name | ascii_downcase) == ($name | ascii_downcase))
					| .name
				)
			) // empty
		' "$label_catalog_file"
	)"
	if [ -n "$existing_name" ]; then
		printf '%s\n' "$existing_name"
		return
	fi

	local create_json created_label created_name updated_catalog
	create_json="$("$kent_bin" task label create "$requested_name" --project . --json)"
	if ! created_label="$(
		jq -ce '
			.label
			| select(
				type == "object"
				and (.name | type == "string")
				and (.name | length > 0)
			)
		' <<<"$create_json"
	)"; then
		fail "Kent created label '$requested_name', but returned an invalid label response."
	fi
	created_name="$(jq -r '.name' <<<"$created_label")"

	updated_catalog="$label_catalog_file.updated"
	jq --argjson label "$created_label" '.catalog.labels += [$label]' "$label_catalog_file" >"$updated_catalog"
	mv "$updated_catalog" "$label_catalog_file"

	echo "Created missing Kent label '$created_name'." >&2
	printf '%s\n' "$created_name"
}

issue_field_option() {
	local field_name="$1"
	local field_file="$2"
	local value
	if ! value="$(
		jq -r --arg field "$field_name" '
			[.[] | select(.issue_field_name == $field)] as $matches
			| if ($matches | length) == 0 then
				""
			elif
				($matches | length) == 1
				and $matches[0].data_type == "single_select"
				and ($matches[0].single_select_option.name | type) == "string"
				and ($matches[0].single_select_option.name | length) > 0
			then
				$matches[0].single_select_option.name
			else
				error("expected zero or one assigned single-select value")
			end
		' "$field_file"
	)"; then
		fail "GitHub returned an invalid '$field_name' issue field value for GH #$number in $repo."
	fi
	printf '%s\n' "$value"
}

priority_label() {
	case "$1" in
	"")
		;;
	Urgent)
		printf '%s\n' "P0"
		;;
	High)
		printf '%s\n' "P1"
		;;
	Medium)
		printf '%s\n' "P2"
		;;
	Low)
		printf '%s\n' "P3"
		;;
	*)
		fail "Unsupported GitHub Priority '$1' on GH #$number in $repo."
		;;
	esac
}

effort_label() {
	case "$1" in
	"")
		;;
	Low)
		printf '%s\n' "Small"
		;;
	Medium)
		printf '%s\n' "Medium"
		;;
	High)
		printf '%s\n' "Large"
		;;
	*)
		fail "Unsupported GitHub Effort '$1' on GH #$number in $repo."
		;;
	esac
}

is_decimal_number() {
	case "$1" in
	"" | *[!0123456789]*)
		return 1
		;;
	*)
		return 0
		;;
	esac
}

expanded_refs=()

expand_token() {
	local token="$1"
	token="${token#"${token%%[![:space:]]*}"}"
	token="${token%"${token##*[![:space:]]}"}"
	[ -n "$token" ] || return 0

	case "$token" in
	*..*)
		local start="${token%%..*}"
		local end="${token##*..}"
		if is_decimal_number "$start" && is_decimal_number "$end"; then
			[ "$start" -le "$end" ] || fail "Invalid range '$token': start is greater than end."
			local i
			for ((i = start; i <= end; i++)); do
				expanded_refs+=("$i")
			done
			return
		fi
		;;
	esac

	expanded_refs+=("$token")
}

expand_args() {
	local arg token
	for arg in "$@"; do
		local IFS=','
		for token in $arg; do
			expand_token "$token"
		done
	done
}

parse_issue_ref() {
	local ref="$1"
	repo=""
	number=""

	if is_decimal_number "$ref"; then
		repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
		number="$ref"
		return
	fi

	local normalized="${ref#http://}"
	normalized="${normalized#https://}"
	normalized="${normalized#www.}"
	normalized="${normalized%%\?*}"
	normalized="${normalized%%#*}"

	case "$normalized" in
	github.com/*/*/issues/*)
		local path="${normalized#github.com/}"
		local owner="${path%%/*}"
		local rest="${path#*/}"
		local repo_name="${rest%%/*}"
		local issue_path="${rest#*/issues/}"
		number="${issue_path%%/*}"
		repo="$owner/$repo_name"
		;;
	*)
		fail "Expected a GitHub issue number or URL, got '$ref'."
		;;
	esac

	if ! is_decimal_number "$number"; then
		fail "Expected a GitHub issue number or URL, got '$ref'."
	fi
}

import_issue() {
	local ref="$1"
	repo=""
	number=""
	parse_issue_ref "$ref"

	local issue_file="$tmpdir/issue.json"
	local issue_fields_file="$tmpdir/issue-fields.json"
	local comments_file="$tmpdir/comments.json"
	local body_file="$tmpdir/body.md"

	gh api "repos/$repo/issues/$number" >"$issue_file"

	if [ "$(jq -r 'has("pull_request")' "$issue_file")" = "true" ]; then
		echo "Skipping GH #$number in $repo: it is a pull request, not an issue." >&2
		return 0
	fi

	gh api "repos/$repo/issues/$number/issue-field-values" >"$issue_fields_file"
	if ! jq -e 'type == "array"' "$issue_fields_file" >/dev/null; then
		fail "GitHub returned invalid issue field values for GH #$number in $repo."
	fi

	local priority effort issue_type
	priority="$(issue_field_option "Priority" "$issue_fields_file")"
	effort="$(issue_field_option "Effort" "$issue_fields_file")"
	issue_type="$(jq -r '.type.name // ""' "$issue_file")"

	local -a requested_labels=()
	local mapped_label repository_label existing_label duplicate
	mapped_label="$(priority_label "$priority")"
	if [ -n "$mapped_label" ]; then
		requested_labels+=("$mapped_label")
	fi
	mapped_label="$(effort_label "$effort")"
	if [ -n "$mapped_label" ]; then
		requested_labels+=("$mapped_label")
	fi
	case "$issue_type" in
	Bug | Feature)
		requested_labels+=("$issue_type")
		;;
	esac
	while IFS= read -r repository_label; do
		[ -n "$repository_label" ] || continue
		duplicate=0
		for existing_label in "${requested_labels[@]}"; do
			if [ "$repository_label" = "$existing_label" ]; then
				duplicate=1
				break
			fi
		done
		if [ "$duplicate" -eq 0 ]; then
			requested_labels+=("$repository_label")
		fi
	done < <(jq -r '.labels[]? | .name // empty' "$issue_file")

	local -a task_labels=()
	local requested_label resolved_label
	for requested_label in "${requested_labels[@]}"; do
		resolved_label="$(ensure_project_label "$requested_label")"
		duplicate=0
		for existing_label in "${task_labels[@]}"; do
			if [ "$resolved_label" = "$existing_label" ]; then
				duplicate=1
				break
			fi
		done
		if [ "$duplicate" -eq 0 ]; then
			task_labels+=("$resolved_label")
		fi
	done

	local title issue_body issue_url import_date
	title="$(jq -r '.title // ""' "$issue_file")"
	issue_body="$(jq -r '.body // ""' "$issue_file")"
	issue_url="$(jq -r '.html_url // ""' "$issue_file")"
	import_date="$(date +%F)"

	{
		if [ -n "$issue_body" ]; then
			printf '%s\n\n' "$issue_body"
		fi
		printf 'imported from GH #%s on %s\n' "$number" "$import_date"
	} >"$body_file"

	local task_title create_json task_ref task_id
	local -a create_args
	task_title="GH #$number: $title"
	create_args=(task create --project . --title "$task_title" --body-file "$body_file" --source-url "$issue_url")
	for resolved_label in "${task_labels[@]}"; do
		create_args+=(--label "$resolved_label")
	done
	create_args+=(--json)
	create_json="$("$kent_bin" "${create_args[@]}")"

	task_ref="$(jq -r '.summary.short_id // ""' <<<"$create_json")"
	task_id="$(jq -r '.summary.task_id // .summary.id // ""' <<<"$create_json")"
	if [ -z "$task_ref" ] || [ "$task_ref" = "null" ]; then
		fail "Imported task was created, but its short id could not be read from kent output."
	fi
	echo "Created Kent task $task_ref ($task_id) for GH #$number."

	gh api --paginate --slurp "repos/$repo/issues/$number/comments" >"$comments_file"

	local comment_count
	comment_count="$(jq '[.[][]] | length' "$comments_file")"
	if [ "$comment_count" -eq 0 ]; then
		echo "Imported GH #$number into $task_ref with no comments."
		return 0
	fi

	local comment_index=0 comment_json comment_author comment_body comment_file
	while IFS= read -r comment_json; do
		comment_index=$((comment_index + 1))
		comment_author="$(jq -r '.user.login // "unknown"' <<<"$comment_json")"
		comment_body="$(jq -r '.body // ""' <<<"$comment_json")"
		comment_file="$tmpdir/comment-$comment_index.md"
		printf '%s\n' "$comment_body" >"$comment_file"
		"$kent_bin" task comment add "$task_ref" --author user --author-id "$comment_author" --body-file "$comment_file"
	done < <(jq -c '.[][]' "$comments_file")

	echo "Imported GH #$number into $task_ref with $comment_count comments."
}

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
kent_bin="${KENT_BIN:-}"
if [ -z "$kent_bin" ]; then
	if [ -x "$repo_root/bin/kent" ]; then
		kent_bin="$repo_root/bin/kent"
	else
		kent_bin="kent"
	fi
fi

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
	usage
	exit 0
fi

if [ "$#" -lt 1 ]; then
	usage
	exit 2
fi

require_command gh
require_command jq
if ! command -v "$kent_bin" >/dev/null 2>&1 && [ ! -x "$kent_bin" ]; then
	fail "kent is required. Set KENT_BIN or build ./bin/kent."
fi

expand_args "$@"
if [ "${#expanded_refs[@]}" -eq 0 ]; then
	usage
	exit 2
fi

tmpdir="$(mktemp -d)"
cleanup() {
	if command -v trash >/dev/null 2>&1; then
		trash "$tmpdir" >/dev/null 2>&1 || true
	else
		find "$tmpdir" -depth -delete >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

label_catalog_file="$tmpdir/project-labels.json"
load_project_label_catalog

repo=""
number=""
for ref in "${expanded_refs[@]}"; do
	import_issue "$ref"
done
