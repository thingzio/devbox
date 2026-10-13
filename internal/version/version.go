// Package version holds build metadata injected with -ldflags.
package version

import "runtime/debug"

// Version is the build version, set at link time by make and goreleaser. A
// `go install module@version` build carries no ldflags, so it falls back to the
// module version Go records in the binary.
var Version = "dev"

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolve(Version, info, ok)
}

func resolve(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "dev" || !ok || info == nil {
		return stamped
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return stamped
}
