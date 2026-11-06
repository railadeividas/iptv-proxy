// Package version holds the version of this build.
package version

import "runtime/debug"

// Version is set at build time: -ldflags "-X iptv-proxy/internal/version.Version=v1.2.0". The release workflow passes the tag;
// a plain `go build` or `go run` leaves it as "dev".
var Version = "dev"

// String is the version to show: the release tag, or for a development build "dev" plus the commit it was built from, if Go knows it.
func String() string {
	if Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return "dev-" + s.Value[:7]
			}
		}
	}
	return Version
}
