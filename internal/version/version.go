// Package version holds the version of this build.
package version

import "runtime/debug"

// Version is set at build time: -ldflags "-X iptv-proxy/internal/version.Version=ef711d2". The publish workflow passes the short
// commit hash of the build (there are no release tags); a plain `go build` or `go run` leaves it as "dev".
var Version = "dev"

// String is the version to show: the commit hash of an image build, or for a development build "dev" plus the commit it was built from, if Go knows it.
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
