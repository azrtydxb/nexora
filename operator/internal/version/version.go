// Package version holds the operator build stamp, set with -ldflags -X.
package version

var (
	// Version is the release (image tag) the operator was built as; "dev" when unstamped.
	Version = "dev"
	// Commit is the git commit the operator was built from.
	Commit = ""
	// BuildDate is the build time.
	BuildDate = ""
)
