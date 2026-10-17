package version

var (
	// Version is the semantic version of the application (e.g. "v1.0.0" or "0.2.0-dev").
	// Injected at build time via:
	//   -ldflags "-X mini-aigw/internal/version.Version=v1.0.0"
	Version = "0.2.0-dev"

	// GitCommit is the commit hash of the git repository.
	// Injected at build time via:
	//   -ldflags "-X mini-aigw/internal/version.GitCommit=abc1234"
	GitCommit = "none"

	// BuildTime is the build timestamp (RFC3339).
	// Injected at build time via:
	//   -ldflags "-X mini-aigw/internal/version.BuildTime=2026-10-18T03:00:00Z"
	BuildTime = "unknown"
)

