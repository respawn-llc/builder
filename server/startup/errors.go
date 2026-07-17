package startup

import "errors"

// errAuthManagerRequired is returned by EnsureReady when the auth state carries
// no auth manager. Callers and tests match it with errors.Is rather than
// comparing rendered message text.
var errAuthManagerRequired = errors.New("auth manager is required")

var ErrOnboardingRequired = errors.New("first-time setup is required")
