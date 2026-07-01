package commands

import (
	"core/prompts"
	"sort"
	"strings"
	"unicode"
)

type Action string

const (
	ActionNone              Action = "none"
	ActionExit              Action = "exit"
	ActionNew               Action = "new"
	ActionResume            Action = "resume"
	ActionLogout            Action = "logout"
	ActionCompact           Action = "compact"
	ActionSetName           Action = "set_name"
	ActionSetThinking       Action = "set_thinking"
	ActionSetFast           Action = "set_fast"
	ActionSetSupervisor     Action = "set_supervisor"
	ActionSetAutoCompaction Action = "set_auto_compaction"
	ActionSetQuestions      Action = "set_questions"
	ActionStatus            Action = "status"
	ActionGoal              Action = "goal"
	ActionProcesses         Action = "processes"
	ActionWorktree          Action = "worktree"
	ActionCopy              Action = "copy"
	ActionBack              Action = "back"
	ActionUnhandled         Action = "unhandled"
)

type GoalMode string

const (
	GoalModeShow   GoalMode = "show"
	GoalModeSet    GoalMode = "set"
	GoalModePause  GoalMode = "pause"
	GoalModeResume GoalMode = "resume"
	GoalModeClear  GoalMode = "clear"
)

type Result struct {
	Handled            bool
	Action             Action
	Text               string
	Args               string
	SubmitUser         bool
	User               string
	FreshConversation  bool
	SessionName        string
	ThinkingLevel      string
	FastMode           string
	SupervisorMode     string
	AutoCompactionMode string
	QuestionsMode      string
	GoalMode           GoalMode
	GoalObjective      string
}

type Handler func(args string) Result

type Command struct {
	Name                       string
	Description                string
	RunWhileBusy               bool
	PreservePromptHistoryDraft bool
}

type registeredCommand struct {
	command Command
	handler Handler
}

type Registry struct {
	handlers map[string]registeredCommand
	aliases  map[string]string
}

func NewRegistry() *Registry {
	return &Registry{handlers: map[string]registeredCommand{}, aliases: map[string]string{}}
}

func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.RegisterWithOptions("exit", "Exit Kent", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionExit}
	})
	r.RegisterWithOptions("new", "Create a new session", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionNew}
	})
	r.RegisterWithOptions("resume", "Go to startup screen (session picker)", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionResume}
	})
	r.RegisterWithOptions("logout", "Open auth options", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionLogout}
	})
	r.RegisterWithOptions("login", "Open auth options", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionLogout}
	})
	r.RegisterWithOptions("compact", "Compact the current context (optional: /compact <instructions>)", RegisterOptions{PreservePromptHistoryDraft: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionCompact, Args: strings.TrimSpace(args)}
	})
	r.RegisterWithOptions("name", "Set session title and terminal title (usage: /name <title>; empty resets)", RegisterOptions{RunWhileBusy: true, PreservePromptHistoryDraft: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionSetName, SessionName: strings.TrimSpace(args)}
	})
	r.RegisterWithOptions("thinking", "Set or show thinking level (usage: /thinking <low|medium|high|xhigh>; empty shows current)", RegisterOptions{RunWhileBusy: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionSetThinking, ThinkingLevel: strings.ToLower(strings.TrimSpace(args))}
	})
	r.RegisterWithOptions("fast", "Toggle Fast mode to request priority inference (usage: /fast [on|off|status]; empty toggles)", RegisterOptions{RunWhileBusy: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionSetFast, FastMode: strings.ToLower(strings.TrimSpace(args))}
	})
	r.RegisterWithOptions("supervisor", "Toggle reviewer invocation (usage: /supervisor [on|off]; empty toggles)", RegisterOptions{RunWhileBusy: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionSetSupervisor, SupervisorMode: strings.ToLower(strings.TrimSpace(args))}
	})
	r.RegisterWithOptions("autocompaction", "Toggle auto-compaction (usage: /autocompaction [on|off]; empty toggles)", RegisterOptions{RunWhileBusy: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionSetAutoCompaction, AutoCompactionMode: strings.ToLower(strings.TrimSpace(args))}
	})
	r.RegisterWithOptions("questions", "Toggle ask_question tool (usage: /questions [on|off]; empty toggles)", RegisterOptions{RunWhileBusy: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionSetQuestions, QuestionsMode: strings.ToLower(strings.TrimSpace(args))}
	})
	r.RegisterWithOptions("status", "Open a detailed status overlay for the current session/runtime", RegisterOptions{RunWhileBusy: true, PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionStatus}
	})
	r.RegisterWithOptions("goal", "Set or manage the current session goal (usage: /goal [show|pause|resume|clear|<objective>])", RegisterOptions{RunWhileBusy: true, PreservePromptHistoryDraft: true}, func(args string) Result {
		mode := GoalModeShow
		objective := strings.TrimSpace(args)
		switch strings.ToLower(objective) {
		case string(GoalModeShow):
			mode = GoalModeShow
			objective = ""
		case string(GoalModePause):
			mode = GoalModePause
			objective = ""
		case string(GoalModeResume):
			mode = GoalModeResume
			objective = ""
		case string(GoalModeClear):
			mode = GoalModeClear
			objective = ""
		default:
			if objective != "" {
				mode = GoalModeSet
			}
		}
		return Result{Handled: true, Action: ActionGoal, GoalMode: mode, GoalObjective: objective}
	})
	r.RegisterWithOptions("ps", "List background processes or manage one (usage: /ps [kill|inline|logs] <id>)", RegisterOptions{RunWhileBusy: true, PreservePromptHistoryDraft: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionProcesses, Args: strings.TrimSpace(args)}
	})
	r.RegisterWithOptions("worktree", "Manage git worktrees (usage: /worktree [create|switch|delete] ...)", RegisterOptions{PreservePromptHistoryDraft: true}, func(args string) Result {
		return Result{Handled: true, Action: ActionWorktree, Args: strings.TrimSpace(args)}
	})
	r.RegisterAlias("wt", "worktree")
	r.RegisterWithOptions("copy", "Copy the last model final answer to the system clipboard", RegisterOptions{RunWhileBusy: true, PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionCopy}
	})
	r.RegisterWithOptions("back", "Jump to parent session if current session was spawned from another", RegisterOptions{PreservePromptHistoryDraft: true}, func(string) Result {
		return Result{Handled: true, Action: ActionBack}
	})
	registerPromptCommands(r, []promptCommandSpec{
		{
			Name:         "review",
			Description:  "Run code review (optional: /review <what to review>)",
			Prompt:       prompts.ReviewPrompt,
			FreshSession: true,
		},
		{
			Name:         "init",
			Description:  "Run repository initialization prompt (optional: /init <instructions>)",
			Prompt:       prompts.InitPrompt,
			FreshSession: true,
		},
	})
	return r
}

type RegisterOptions struct {
	RunWhileBusy               bool
	PreservePromptHistoryDraft bool
}

func (r *Registry) RegisterWithOptions(name string, description string, options RegisterOptions, h Handler) {
	if r == nil || h == nil {
		return
	}
	k := normalizeCommandName(name)
	if k == "" {
		return
	}
	r.handlers[k] = registeredCommand{
		command: Command{Name: k, Description: strings.TrimSpace(description), RunWhileBusy: options.RunWhileBusy, PreservePromptHistoryDraft: options.PreservePromptHistoryDraft},
		handler: h,
	}
}

func (r *Registry) RegisterAlias(alias string, target string) {
	if r == nil {
		return
	}
	aliasKey := normalizeCommandName(alias)
	targetKey := normalizeCommandName(target)
	if aliasKey == "" || targetKey == "" {
		return
	}
	if aliasKey == targetKey {
		return
	}
	r.aliases[aliasKey] = targetKey
}

func (r *Registry) Commands() []Command {
	if r == nil {
		return nil
	}
	list := make([]Command, 0, len(r.handlers))
	for _, entry := range r.handlers {
		list = append(list, entry.command)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

func (r *Registry) Match(query string) []Command {
	if r == nil {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(query))
	type scoredCommand struct {
		command Command
		index   int
	}
	scored := make([]scoredCommand, 0, len(r.handlers))
	for _, entry := range r.handlers {
		idx := 0
		if normalized != "" {
			idx = strings.Index(entry.command.Name, normalized)
			if idx < 0 {
				continue
			}
		}
		scored = append(scored, scoredCommand{command: entry.command, index: idx})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].index != scored[j].index {
			return scored[i].index < scored[j].index
		}
		if len(scored[i].command.Name) != len(scored[j].command.Name) {
			return len(scored[i].command.Name) < len(scored[j].command.Name)
		}
		return scored[i].command.Name < scored[j].command.Name
	})
	out := make([]Command, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.command)
	}
	return out
}

func (r *Registry) Parse(raw string) (name string, args string, isCommand bool) {
	if r == nil {
		return "", "", false
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed[0] != '/' {
		return "", "", false
	}
	payload := strings.TrimSpace(trimmed[1:])
	if payload == "" {
		return "", "", true
	}
	parts := strings.Fields(payload)
	name = strings.ToLower(parts[0])
	if len(parts) > 1 {
		args = strings.TrimSpace(strings.TrimPrefix(payload, parts[0]))
	}
	return name, args, true
}

func (r *Registry) Execute(raw string) Result {
	name, args, ok := r.Parse(raw)
	if !ok {
		return Result{Handled: false, Action: ActionUnhandled}
	}
	if name == "" {
		return Result{Handled: false, Action: ActionUnhandled}
	}
	registered, exists := r.lookupRegistered(name)
	if !exists {
		return Result{Handled: false, Action: ActionUnhandled}
	}
	res := registered.handler(args)
	res.Handled = true
	return res
}

func (r *Registry) Command(raw string) (Command, bool) {
	name, _, ok := r.Parse(raw)
	if !ok || name == "" {
		return Command{}, false
	}
	registered, exists := r.lookupRegistered(name)
	if !exists {
		return Command{}, false
	}
	return registered.command, true
}

func (r *Registry) lookupRegistered(name string) (registeredCommand, bool) {
	if r == nil {
		return registeredCommand{}, false
	}
	resolvedName := r.resolveAlias(name)
	registered, exists := r.handlers[resolvedName]
	if !exists {
		return registeredCommand{}, false
	}
	return registered, true
}

func (r *Registry) resolveAlias(name string) string {
	if r == nil {
		return ""
	}
	normalized := normalizeCommandName(name)
	if normalized == "" {
		return ""
	}
	if target, ok := r.aliases[normalized]; ok {
		return target
	}
	return normalized
}

func normalizeCommandName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return ""
	}
	if strings.IndexFunc(normalized, unicode.IsSpace) >= 0 {
		panic("slash command names must not contain whitespace")
	}
	return normalized
}
