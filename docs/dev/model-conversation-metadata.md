# Model-facing conversation metadata for OAI models

Role, channel, recipient, and tool namespace are distinct concepts. Use lowercase `user` consistently in model-facing instructions.

## Roles and content

Kent's [message contracts](../../server/llm/types.go) define the roles `system`, `developer`, `user`, `assistant`, and `tool`.

The agent reports receiving a message's role and content separately. For example, this session's format-check message can be represented as:

```text
Role: user
Content: can you show me exactly, for this specific message? so we establish the correct format
```

## Channels

The environment for OAI gpt-6 lists:

- `analysis`: internal reasoning.
- `commentary`: user-visible updates and the channel used for tool calls in this session.
- `final`: the assistant's final response.
- `summary`: listed by the environment, but not an agent-authored output channel according to the operator's finding below.

These environment channels are not all Kent message phases. Kent's [message contracts](../../server/llm/types.go) expose `commentary` and `final` as message phases.

### Operator finding about `summary`

`summary` is populated by another model and cannot be used for agent-authored output:

1. Model streams "i'm gonna try now:"
2. model struggles for 20 seconds with no new text
3. Suddenly #1 disappears
4. final response replaces streamed response with an unsubstantiated claim like "i can't actually do it, I was wrong".

## Recipients and tool namespaces

A role identifies the speaker. A recipient identifies the destination of a message. A tool namespace groups callable tools.

For example, a tool call in this session has:

```text
Role: assistant
Channel: commentary
Recipient: functions.exec_command
Namespace: functions
Tool: exec_command
```

The namespaces exposed in this session are:

- `functions`: Kent tools, such as `functions.exec_command` and `functions.ask_question`.
- `multi_tool_use`: the parallel-call wrapper, `multi_tool_use.parallel`.
- `web`: browsing and lookup through `web.run`.
