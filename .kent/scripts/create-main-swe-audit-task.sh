#!/usr/bin/env bash
set -euo pipefail

readonly main_swe_workflow_id="e77b0405-7d91-4167-a7f7-b868e28b4edb"

die() {
    echo "$*" >&2
    exit 1
}

find_kent() {
    local candidate

    if candidate="$(command -v kent 2>/dev/null)" && [[ -x "$candidate" ]]; then
        printf '%s\n' "$candidate"
        return
    fi

    for candidate in \
        "$HOME/.local/bin/kent" \
        "/opt/homebrew/bin/kent" \
        "/usr/local/bin/kent"; do
        if [[ -x "$candidate" ]]; then
            printf '%s\n' "$candidate"
            return
        fi
    done

    die "kent executable was not found"
}

read_required_string() {
    local input="$1"
    local key="$2"

    jq -er \
        --arg key "$key" \
        '.[$key] | select(type == "string" and length > 0)' \
        <<<"$input" ||
        die "workflow input $key must be a non-empty string"
}

command -v jq >/dev/null 2>&1 || die "jq is required"
[[ -n "${KENT_EXECUTION_ROOT:-}" ]] || die "KENT_EXECUTION_ROOT is required"

input="$(jq -ce 'if type == "object" then . else error("workflow stdin must be a JSON object") end')"
task_title="$(read_required_string "$input" "task_title")"
task_body="$(read_required_string "$input" "task_body")"

if ((${#task_title} > 40)); then
    die "workflow input task_title must contain at most 40 characters"
fi

kent_bin="$(find_kent)"
created="$(
    "$kent_bin" task create \
        --project "$KENT_EXECUTION_ROOT" \
        --workflow "$main_swe_workflow_id" \
        --source-workspace "$KENT_EXECUTION_ROOT" \
        --title "$task_title" \
        --body "$task_body" \
        --json
)"

short_id="$(jq -er '.summary.short_id | strings | select(length > 0)' <<<"$created")" ||
    die "created task response omitted summary.short_id"
created_workflow_id="$(jq -er '.workflow.workflow_id | strings | select(length > 0)' <<<"$created")" ||
    die "created task response omitted workflow.workflow_id"

[[ "$created_workflow_id" == "$main_swe_workflow_id" ]] ||
    die "created task $short_id on workflow $created_workflow_id instead of Main SWE"

jq -n \
    --arg short_id "$short_id" \
    '{
        transition: "done",
        commentary: ("Created " + $short_id + " in Main SWE Backlog.")
    }'
