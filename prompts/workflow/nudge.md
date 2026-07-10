Plaintext `final_answer` responses or outputs that do not match the schema are **not** valid completions of tasks, so your response was rejected. Exact reason:
{{.RejectionReason}}.

If the task you received is not yet done, **keep working**, do not stop with `final_answer` anymore until you're ready to submit your work as completed.

Instructions for completing this node:
{{.NodeCompletionInstructions}}

{{- if .GoalText }}

{{.GoalText}}
{{- end }}

{{- if .GoalReminder }}

{{.GoalReminder}}
{{- end }}
