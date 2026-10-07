package skills

// Scope identifies the provenance layer a skill was discovered in.
// Precedence (highest first): project, user, plugin, builtin, system.
type Scope string

const (
	// ScopeSystem is shipped inside the host binary and released to disk.
	ScopeSystem Scope = "system"
	// ScopeBuiltin is synced from a remote builtin catalog.
	ScopeBuiltin Scope = "builtin"
	// ScopePlugin is contributed by an installed plugin.
	ScopePlugin Scope = "plugin"
	// ScopeUser lives in the user-level skills home.
	ScopeUser Scope = "user"
	// ScopeProject lives inside the current project/workspace.
	ScopeProject Scope = "project"
)
