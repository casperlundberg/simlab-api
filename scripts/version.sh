#!/usr/bin/env bash
#
# Prints this checkout's semantic version, derived from its release tags.
#
#   on the tag v1.2.0                      1.2.0
#   three commits after v1.2.0             1.2.1-dev.3+abc1234
#   no release yet                         0.0.0-dev.N+abc1234   (N: commits in history)
#   uncommitted or untracked changes       ...+abc1234.dirty
#
# A development build is a pre-release of the release it leads to, so it sorts
# after the release it came from and before that one, as SemVer orders them.
# Which release that is, CHANGELOG.md says: a section headed "## 2.0.0 —
# unreleased" makes every build after v1.2.0 a 2.0.0-dev. Without one — or with
# one no higher than the last release, left over from it — it is the next
# patch. Guessing the patch for a breaking change would stamp a MAJOR change
# with a version that promises nothing broke, on every result produced by it.
# The commit goes in the build metadata, which SemVer ignores for ordering but
# which is what makes a build traceable to exactly one tree.
#
# Only plain vMAJOR.MINOR.PATCH tags are releases. Usage: version.sh [dir]
set -euo pipefail

cd "${1:-.}"

hash="$(git rev-parse --short=7 HEAD)"
dirty=""
# The same test Go's own build stamp uses, untracked files included: a file
# nobody committed can still be compiled in.
if [[ -n "$(git status --porcelain)" ]]; then
  dirty=".dirty"
fi

# The version the changelog's unreleased section names, if it has one.
unreleased=""
if [[ -f CHANGELOG.md ]]; then
  unreleased="$(grep -m1 -iE '^## \[?[0-9]+\.[0-9]+\.[0-9]+\]? +(—|-) +unreleased *$' CHANGELOG.md \
    | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
fi

tag="$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' 2>/dev/null || true)"
if [[ -z "$tag" ]]; then
  echo "${unreleased:-0.0.0}-dev.$(git rev-list --count HEAD)+${hash}${dirty}"
  exit 0
fi

release="${tag#v}"
count="$(git rev-list --count "${tag}..HEAD")"
if [[ "$count" == "0" ]]; then
  if [[ -n "$dirty" ]]; then
    echo "${release}+${hash}${dirty}"
  else
    echo "$release"
  fi
  exit 0
fi

IFS=. read -r major minor patch <<< "$release"
next="${major}.${minor}.$((patch + 1))"
if [[ -n "$unreleased" && "$unreleased" != "$release" \
      && "$(printf '%s\n%s\n' "$release" "$unreleased" | sort -V | tail -1)" == "$unreleased" ]]; then
  next="$unreleased"
fi
echo "${next}-dev.${count}+${hash}${dirty}"
