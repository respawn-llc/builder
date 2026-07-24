Heads up: you just received a new task for the ticket `{{.TaskShortId}}`:
<task>
{{.NodePrompt}}
</task>

Some time has passed since you completed the previous task, so your workspace state might have changed.
Focus on the new information: what is different about the new task and workspace state, and what you need to do to complete it without redoing the previous work. For example, you might need to re-review or re-check the data against new evidence if you received another request.

## Reminder about workflow mode guidelines
- For substantial work, maintain durable files containing concrete action item checklists across handoffs and mark each item done in order, focusing on one subtask at a time.
- You **can** still use `ask_question` in this mode, and the user will still answer. However, they aren't directly monitoring your work, so avoid giving updates, commentary, or issuing preambles.
- Do not use `NO_OP` in workflow mode. If you need to wait on a running command, poll it with `write_stdin`.
- Use `{{.LaunchCommand}} task` to interact with tickets (add new tickets, update the current ticket, leave comments, etc.). Example: `{{.LaunchCommand}} task show {{.TaskShortId}}` will show the overall ticket context.
- Prefer evidence from files, commands, tests, docs, and runtime output over assumptions.
- If requirements are unclear, ask the user instead of guessing.
- If blocked, report the blocker and the smallest useful next step via `ask_question`.
- Do not treat partial implementation, intent, elapsed effort, or unrelated passing tests as proof.
- Your job isn't to complete the entire ticket - only the task you received.

{{- if .ShowTaskCommentsReminder }}
This ticket now has {{.TaskCommentsLabel}}. You can run `{{.TaskCommentListCommand}}` to read the comments if they are relevant or if new ones have surfaced.
{{- end }}

## How to mark the new task complete
{{.NodeCompletionInstructions}}

Once you've finished the new task, you can execute the transition again. It is normal to complete multiple tasks in one session.

{{- if gt (len .Transitions) 1 }}
Multiple transitions are available for the new task, so you decide what status to move this ticket to after you complete your task. Pick the transition ID from the list that best fits the criteria:
{{- range .Transitions }}
- {{.ID}}{{if .DisplayName}} ({{.DisplayName}}){{end}}{{if .Description}}: {{.Description}}{{end}}
{{- end }}
{{- end }}

Proceed to completion of the new task now.
