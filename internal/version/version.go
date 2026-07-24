// Package version exposes build-time version information for both binaries.
package version

// Version is replaced by release builds through -ldflags. Development builds
// intentionally retain "dev".
var Version = "dev"
