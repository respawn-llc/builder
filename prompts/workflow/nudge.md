Workflow completion was rejected. Retry with valid workflow completion output only.

Rejection reason:
{{.RejectionReason}}

Instructions for completing this node:
{{.NodeCompletionInstructions}}

{{- if .GoalReminder }}

{{.GoalReminder}}
{{- end }}
