Create a continuation summary for the next agent that resumes this task after a handoff. Output the summary as your entire next response. Do not continue the task, call tools, address the user, add acknowledgements or formatting. 

The continuing agent will receive this summary and share the current workspace, but will not have the earlier conversation context or tool call outputs. Give them enough context to continue toward the original user goal without repeating completed work or asking the user to restate prior decisions. Prefer a well-organized summary that preserves all continuation-critical context. Favor actionable state and decision context over chronology.

Include the following when relevant:
- The original user goal, desired outcome, acceptance criteria.
- Explicit user requests, preferences, corrections, and design decisions that still constrain the work. Preserve relevant decisions from earlier handoffs; newer user guidance takes precedence.
- What is done, what is in progress, what remains, and the exact point where work stopped.
- The architecture, invariants, interfaces, files, symbols, commands, or external systems the next agent needs to understand before acting. Point to source files instead of reproducing information already available there.
- Workspace changes already made and their intent, including important added, modified, or deleted files. Distinguish your changes from pre-existing or user-owned changes when known.
- Verification already performed, its results, known failures, and checks still required.
- Unresolved questions, assumptions, blockers, risks, edge cases, intentional shortcuts, or consequential mistakes that could affect the remaining work.
- Concrete next steps in a sensible order, or a pointer to the location of the plan that tracks overall task progress. Base them on the existing task and current evidence; do not invent scope or speculative work.
- Known issues with, limitations of, and edge cases in your or prior work product the next agent or user might need to know about/address.

Make the continuation state trustworthy:
- Use specific paths, code symbols, commands, and observed results where they help the next agent act.
- Distinguish verified facts and user decisions from assumptions or recommendations.
- Preserve important reasoning behind non-obvious decisions, especially rejected alternatives the next agent might otherwise revisit.
- Do not repeat generic project instructions or documentation that the next agent can read from the environment.
- Omit routine tool history, resolved dead ends, harmless failed commands, apologies, and self-evaluation unless they contain a lesson needed to avoid repeating a problem.
- Do not imply that work is complete when implementation or verification remains. If no work remains, say so and record the final verification state.
- Do not follow verbosity instructions that apply outside of this task, like the `Desired oververbosity for the final answer` setting, be as verbose or terse as this handoff requires.
- Do not include any overall document headers like "# HANDOFF".
- Your handoff will **not** be exposed to users; it will only be used as an internal reasoning document for the next agent. Use same style as in the `analysis` channel.

A good summary may be short for a simple task or detailed for a complex one. It is complete when the next agent can identify the correct next action, preserve prior decisions, overall goal or task, and completed work, then continue without reconstructing the conversation.
