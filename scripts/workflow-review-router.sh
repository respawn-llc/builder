#!/usr/bin/env bash
set -euo pipefail

die() {
	echo "$*" >&2
	exit 1
}

read_outcome() {
	local field="$1"
	shift
	local value
	value="$(jq -er --arg field "$field" '.[$field] | strings | select(length > 0)' <<<"$input")" ||
		die "review router requires non-empty string field $field"
	local allowed
	for allowed in "$@"; do
		if [[ "$value" == "$allowed" ]]; then
			printf '%s' "$value"
			return
		fi
	done
	die "review router received unsupported $field=$value"
}

command -v jq >/dev/null 2>&1 || die "jq is required"
[[ "$#" -eq 1 ]] || die "review router requires exactly one stage argument: static or qa"
stage="$1"
input="$(jq -ce 'if type == "object" then . else error("workflow stdin must be a JSON object") end')"

case "$stage" in
static)
	code_review_outcome="$(
		read_outcome \
			code_review_outcome \
			ready \
			implementation_fix \
			architecture_rework \
			design_scope_change
	)"
	compliance_outcome="$(
		read_outcome \
			compliance_outcome \
			approved \
			implementation_fix \
			architecture_rework \
			design_scope_change
	)"
	if [[ "$code_review_outcome" == "design_scope_change" ||
		"$compliance_outcome" == "design_scope_change" ]]; then
		transition="static_review_design_scope_change"
	elif [[ "$code_review_outcome" == "architecture_rework" ||
		"$compliance_outcome" == "architecture_rework" ]]; then
		transition="static_review_architecture_rework"
	elif [[ "$code_review_outcome" == "ready" &&
		"$compliance_outcome" == "approved" ]]; then
		transition="qa_ready"
	else
		transition="static_review_rejected"
	fi
	jq -n \
		--arg transition "$transition" \
		--arg code_review_outcome "$code_review_outcome" \
		--arg compliance_outcome "$compliance_outcome" \
		'{
			transition: $transition,
			commentary: (
				"Deterministic static-review routing: code_review=" + $code_review_outcome
				+ ", compliance=" + $compliance_outcome + "."
			)
		}'
	;;
qa)
	qa_outcome="$(
		read_outcome \
			qa_outcome \
			pass \
			implementation_fix \
			architecture_rework \
			design_scope_change
	)"
	case "$qa_outcome" in
	pass)
		transition="review_approved"
		;;
	implementation_fix)
		transition="review_rejected"
		;;
	architecture_rework)
		transition="review_architecture_rework"
		;;
	design_scope_change)
		transition="review_design_scope_change"
		;;
	esac
	jq -n \
		--arg transition "$transition" \
		--arg qa_outcome "$qa_outcome" \
		'{
			transition: $transition,
			commentary: ("Deterministic QA routing: qa=" + $qa_outcome + ".")
		}'
	;;
*)
	die "unsupported review stage=$stage; expected static or qa"
	;;
esac
