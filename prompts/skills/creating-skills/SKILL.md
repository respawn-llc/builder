---
name: creating-skills
description: Create or improve agent skills. Use when the user wants to add a new skill or update an existing skill.
---

Skills are a specialized technical documentation standard intended for AI Agents to read on-demand, and designed to teach them a specific technology, tool, or approach that is **outside of their training data** (aka "knowledge cutoff"). Agents learn about skills when they see the injected frontmatter description, just like you did in this session for this skill. When agents need the skill, they read SKILL.md and change their behavior to follow skill instructions, just like you are doing right now.

Kent discovers skills from these roots (desc priority order):

- `<workspace>/.kent/skills` - use workspace skills for project-specific workflows, repository conventions, local tools, or instructions that should travel with a codebase.
- `~/.kent/skills` - use global skills for reusable personal workflows that apply across all projects.
- `~/.kent/.generated/skills` - Skills in ~/.kent/.generated/ are ephemeral, do not attempt to edit them or add new ones there.

When the user asks to create a skill and it's not evident if it's global or local, use `ask_question` to ask for scope.

## Structure
A skill is a directory with a required `SKILL.md` file:

```text
my-skill/
├─ SKILL.md
├─ scripts/
├─ references/
├─ assets/
└─ ...other...
```

Use optional directories/files when they reduce context or make repeatable work deterministic:

- `scripts/`: executable or interpreter-run scripts for deterministic, repetitive, or noisy tasks.
- `references/`: longer docs the model should read only when needed.
- `assets/`: templates, examples, or static files used by the skill.

Keep `SKILL.md` as the entry point. Put trigger guidance in frontmatter, core workflow in the body, and large variant-specific detail in referenced files.

## Frontmatter
`SKILL.md` starts with YAML frontmatter:

```markdown
---
name: my-skill
description: Do a specific workflow. Use when the user asks for concrete trigger phrases or contexts.
---
```

`name` and `description` are required. Use a stable, lowercase, unique, kebab-case `name`.

### How to Write Description
Write `description` as a trigger condition. Keep description to 1-3 terse sentences, 1-2 lines, and no formatting, because users may have many skills each contributing to the load on your memory.

- Include what the skill does and when to use it. Mention concrete task types, workflows, or domains that should activate the skill.
- **Do not repeat trigger rules** in the SKILL.md body text.
- Speak in terms of practical work tasks when the skill will be useful. Use terms generic enough that the skill will be triggered when the **task type** or **intent** matches where the tool might be useful, but not generic enough that it will be ambiguous as to when to use the skill, and not specific enough that the skill will be sitting idle.

BAD: "wterm is an npm package. Read when tasks mention wterm, @wterm/dom, @wterm/core, PTY-over-WebSocket harnesses" (why: this is too specific and unclear, the skill will not be triggered often enough, because wterm is a general automation tool but description only mentions narrow packages)
GOOD: "wterm is a tool for browser-based TUI automation. Use to perform manual TUI QA, verify your TUI changes, check TUI layouts, or reproduce bugs in a real terminal."

Assume agents like you don't like to use skills unless told to - simple explanations won't trigger usage by themselves, so descriptions need clear instructions. If the user complains that a skill is not used often enough, include trigger words like "autonomously", "proactively use", "prefer", "must" in descriptions.

## Skill content
Follow these rules for authoring skill docs (especially SKILL.md).

Overall, treat writing skills like public developer documentation or guidance. Ask yourself a question when designing a skill: "If a developer saw this tool/repository/workflow/task for the first time, what would they need to know to accomplish/use it effectively?".

- **Do not repeat trigger rules** in the SKILL.md body text.
- Important: Do not document anything that you as a model already know - generic APIs of widely known tools you already remember, basic coding guidelines, widespread tools (like git, docker, jetpack compose, java). If the user asks for a skill for a thing you already know / widespread / too generic, assume they are mistaken and explain that you already know the tool. If they insist, proceed with minimal content that does not duplicate known info, like maybe repo-specific patterns observed in real code, specific user preferences, niche tooling stack, repo-scoped architecture guidelines, new modern APIs you don't yet know, or similar.
- Important: Do not include in skills anything that can quickly become outdated or info that isn't practically useful for completion of tasks, or that only documents events. Assume skills are updated once a year or more rarely. Do not include temporal data, taken decisions, or explainers of your behavior anywhere in the skill. Avoid mentioning in the skill files any user requests that pertain to this conversation or other guidance/feedback you received in this conversation.
  - Bad: "Per user instruction, corrected this skill to explain Decompose Components".
  - Bad: "Added section about committing as requested".
  - Bad: "Introduced Decompose on April 29th in commit `abcdef`".
  - Bad: <User complains about wrong TDD approach>, you write "Encoding correct TDD patterns, not shallow assertions" (in an attempt to appease the user, but as a result encoding irrelevant emotional statement in docs)
  - Bad: User mentions "skill should apply outside this codebase" -> you write "This works especially well outside the original codebase" (You encoded irrelevant info from the user prompt in public documentation)
- Don't praise or explain what you're writing by contrasting it with an implied worse alternative.
  - Bad: "Guide to effective test writing, not shallow coverage pumping". Good: "Testing with Kotest"
- Do not include a global H1 header like `# My Skill`. Do not add extra blank lines immediately after a header line.
- Do not use eye-candy formatting, emoji, tables, decorative file trees, or fancy diagrams that contain a lot of symbols. Skills are read by AIs, not humans.
- Do not include large code examples, or API docs in SKILL.md. Generated, third-party, or optional content like templates / API docs lives either as a reference to SSOT, or in adjacent directories. Skills rot just as much as any documentation, so referring to a source is better than duplicating its content.
- Keep SKILL.md under ~300 lines of markdown text. If some content doesn't fit, reference remaining guidance by topic in SKILL.md and use paths relative to the SKILL.md-containing directory (aka "skill dir"), turning SKILL.md into a summary + doc index.
- Keep source-of-truth details in their owning docs or commands; link or delegate instead of copying long chunks of text.
- Avoid repeating CLI help text, public docs, API docs, or web content verbatim when the reader can read the source of truth directly, and do point to those sources for discovery.
- For workspace skills, point to files in the skill directory and the repository (workspace) only, because this skill may be shared via git and local files will not be accessible. Either include a file directly in the skill folder or point to a public web link. For global skills, avoid pointing to any machine-local files outside the skill dir. Bad: "Example query at ~/Desktop/sample.sql" (this is local user file that might be gone later or not transferred if the user decides to send the skill to someone else). Good: "Kent docs index is at kent.sh/llms.txt", use curl -S to retrieve".
- Assume skills are shared across developers, used on different machines, and public on the internet. Avoid PII, credentials, names of humans, local references.
- Scripts are needed for something actually meaningfully codifying/automating a task, or meaningfully reducing the **amount of input/output** to be manually processed by the skill user. For example, for a merge request review/respond skill, you might include a self-contained, one-shot, flexible script to retrieve all existing inline review comments that will eliminate verbose GraphQL calls. For a docs writing skill, if the doc follows a strict template, you can include a validator script, or a script that sets up a skeleton. Don't create scripts or write code in skills "just in case" or for "what might be useful" before you know it's needed.
- Do not apply any oververbosity parameters or other verbosity instructions you received before when writing skill doc files. Do not omit info to be able to one-shot the content with one tool call; write the file in chunks instead.
- Skills are loaded into your memory, and as you know, it is limited. Respect future agents who will read your skill - avoid fluttery, long-winded explanations, lyrical digressions, maintain high information density throughout the skill.
- Do not include in skills any installation or bootstrap instructions, initial setup guidance, or one-time actions that are not repeated often, unless the skill is wholly and explicitly about something one-time. For example, don't include "Migrating to Kent 2.0" section in a `kent-tasks` skill, but do include it in a `kent-2.0-migration` skill.

## Enabling/disabling skills
To disable or enable a skill, edit its config property instead of deleting the files directly, especially for skills in `.generated/` dir.

```toml
# in ~/.kent/config.toml :
[skills]
"skill-name" = false
```

More info in the `kent-dogfooding` skill, if available, or official docs.

## Creation Workflow
1. Check existing skills so the new one does not duplicate or conflict with them.
2. Identify the scope: workspace or global.
3. Choose a stable directory name and frontmatter `name`.
4. Draft a trigger-focused `description`.
5. Write the smallest useful `SKILL.md` body.
6. If SKILL.md did not encompass the entire topic, write adjacent files.
7. If skill needs reusable scripts, create and manually test them.
8. Double-check that SKILL.md does not exceed 300 lines, no temporal references or fluff were left, and each file in the skill folder is mentioned at least in one place.
9. Preferably, run a subagent to split-brain review your created for adherence to the guidance listed in this `creating-skills` skill.
