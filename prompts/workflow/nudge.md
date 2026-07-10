Plaintext `final_answer` responses or outputs that do not match the schema are **not** valid completions of tasks, so your response was rejected. Exact reason:
{{.RejectionReason}}.

If the task you received is not yet fully done, **keep working**. Do not stop with `final_answer` until you're ready to submit your completed work. When you are, mark the task as completed:
{{.NodeCompletionInstructions}}

Work mode:
- For substantial work, maintain durable files containing concrete action item checklists across handoffs and mark each item done in order, focusing on one subtask at a time.
- Avoid repeating work already completed for this task.
- Prefer evidence from files, commands, tests, docs, and runtime output over assumptions.
- If requirements are unclear, ask the operator instead of guessing.
- If blocked, report the blocker and the smallest useful next step via `ask_question`.

{{- if .GoalText }}
You also have an active goal that was set via `{{.LaunchCommand}} goal`:
```text
{{.GoalText}}
```
Complete the goal before reporting task completion.
{{- end }}
