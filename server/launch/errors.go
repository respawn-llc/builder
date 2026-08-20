package launch

import "errors"

// Sentinel errors produced by the session planner. Callers and tests match
// these with errors.Is rather than comparing rendered message text. Dynamic
// context (e.g. the offending role) is attached via fmt.Errorf("... %w", Err...).
var (
	// ErrPatchEditToolsConflict is returned when both tools.patch and tools.edit
	// are enabled, which is unsupported. It is exported because callers such as
	// server/sessionlaunch plan sessions through the planner and assert on this
	// condition.
	ErrPatchEditToolsConflict = errors.New("tools.patch and tools.edit cannot both be enabled; set one to false")

	// errSessionLaunchIntentRequired is returned when the launch intent kind is
	// absent or invalid.
	errSessionLaunchIntentRequired = errors.New("session launch intent is required")

	// errInvalidAgentRole is returned when an agent-role override does not
	// resolve to a usable subagent role.
	errInvalidAgentRole = errors.New("invalid agent role")
)
