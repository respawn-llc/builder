Workflow completion was rejected. Retry with valid workflow completion output only.

{{- if .RejectionReason }}

Rejection reason:
{{.RejectionReason}}
{{- end }}

{{- if .NodeCompletionInstructions }}

Instructions for completing this node:
{{.NodeCompletionInstructions}}
{{- end }}

{{- if .GoalReminder }}

{{.GoalReminder}}
{{- end }}
