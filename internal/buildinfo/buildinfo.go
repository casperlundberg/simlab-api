// Package buildinfo is which code this process is: the release it was built
// as, and the commit it was built from.
//
// It exists so a run can name the code that produced it. A result that says
// only "simlab-api" cannot be reproduced once simlab-api has changed; one that
// names a commit can be rebuilt from it.
package buildinfo

import (
	"runtime"
	"runtime/debug"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Stamped at link time, by the Makefile and the Dockerfile:
//
//	-X github.com/casperlundberg/simlab-api/internal/buildinfo.version=1.3.0
//	-X github.com/casperlundberg/simlab-api/internal/buildinfo.commit=<full hash>
//	-X github.com/casperlundberg/simlab-api/internal/buildinfo.modified=false
var (
	version  string
	commit   string
	modified string
)

// Read is this process's build.
func Read() domain.Build {
	return read(version, commit, modified, debug.ReadBuildInfo)
}

// read prefers the stamp and falls back to what Go records itself. `make build`
// and CI always stamp. The fallback is for a build that did not: `go build
// -buildvcs=true` in a checkout embeds the commit and whether the tree was
// clean. Go's default does not reliably do so, so a build meant to be traced
// should be stamped rather than rely on it; a Docker build has no .git at all.
func read(version, commit, modified string, recorded func() (*debug.BuildInfo, bool)) domain.Build {
	build := domain.Build{
		Version: version, Commit: commit, Modified: modified == "true",
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}

	info, ok := recorded()
	if !ok || info == nil {
		return build
	}
	if info.GoVersion != "" {
		build.GoVersion = info.GoVersion
	}
	if commit != "" {
		return build
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			build.Commit = setting.Value
		case "vcs.modified":
			build.Modified = setting.Value == "true"
		}
	}
	return build
}
