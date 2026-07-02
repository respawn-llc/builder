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

	// errSessionSelectionRequired is returned when neither selected_session_id
	// nor force_new_session is provided.
	errSessionSelectionRequired = errors.New("selected_session_id or force_new_session is required")

	// errInvalidAgentRole is returned when an agent-role override does not
	// resolve to a usable subagent role.
	errInvalidAgentRole = errors.New("invalid agent role")

	// errLockedAgentRoleChange is returned when a resumed session with a locked
	// model request shape is explicitly reopened with a different agent role.
	errLockedAgentRoleChange = errors.New("locked session agent role cannot be changed")
)
