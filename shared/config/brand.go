// Package brand is the single source of truth for product identity.
//
// Every user-visible name, on-disk directory, environment-variable prefix,
// OS service identifier, and distribution URL derives from the constants here.
// Keeping identity centralized means a future rebrand (or this one) is a
// localized change instead of a repository-wide string sweep, and prevents the
// identity values from drifting out of sync across subsystems.
//
// Values that are intentionally NOT here:
//   - The old "builder" identifiers retained only for migration shims live with
//     the migration tooling, not as product identity.
//   - The native desktop bundle identifier ("sh.kent") is owned by the Tauri
//     config; it is already final and platform-bound.
package config

// Product identity.
const (
	// Product is the human-facing display name used in UI, help, and prompts.
	Product = "Kent"
	// Command is the CLI executable and primary command name.
	Command = "kent"
)

// On-disk identity. These name the directories and marker files the product
// owns under the user's home and inside each workspace.
const (
	// ConfigDirName is the per-home and per-workspace config/state directory.
	ConfigDirName = ".kent"
	// PersistenceRoot is the default home persistence root in tilde form.
	PersistenceRoot = "~/" + ConfigDirName
	// GeneratedMarkerName marks a generated-assets tree as owned by the product.
	GeneratedMarkerName = ".kent-generated.json"
)

// Environment identity. Every configuration environment variable is the prefix
// joined with a SCREAMING_SNAKE_CASE suffix.
const (
	// EnvPrefix prefixes all configuration environment variables.
	EnvPrefix = "KENT_"
	// SessionIDEnv names the environment variable that targets a session.
	SessionIDEnv = EnvPrefix + "SESSION_ID"
)

// OS service identity. New installs register under the "sh.kent" namespace that
// matches the desktop bundle identifier. The old "builder" service IDs are not
// referenced here; only the migration tooling knows them, to tear them down.
const (
	// ServiceDisplayName is the human-facing background-service label.
	ServiceDisplayName = Product + " background service"
	// ServiceLaunchdLabel is the macOS launchd label (and plist base name).
	ServiceLaunchdLabel = "sh.kent.server"
	// ServiceSystemdUnitName is the Linux systemd user-unit name.
	ServiceSystemdUnitName = "kent.service"
	// ServiceWindowsTaskName is the legacy Windows Scheduled Task name, retained
	// only so installs can tear down the pre-SCM scheduled task / Startup item.
	ServiceWindowsTaskName = Product + " Server"
	// ServiceWindowsServiceName is the Windows Service Control Manager service
	// name for the SCM-backed background service.
	ServiceWindowsServiceName = Product + "Server"
)

// Distribution identity. Repository, docs, and package-manager coordinates.
const (
	// RepoSlug is the "owner/name" GitHub repository slug.
	RepoSlug = "respawn-llc/kent"
	// RepoURL is the canonical GitHub repository URL.
	RepoURL = "https://github.com/" + RepoSlug
	// DocsURL is the canonical documentation site root.
	DocsURL = "https://kent.sh"
	// HomebrewTap is the Homebrew tap repository slug.
	HomebrewTap = "respawn-llc/homebrew-tap"
	// HomebrewFormula is the Homebrew formula name for new installs.
	HomebrewFormula = "kent"
)
