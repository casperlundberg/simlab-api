package domain

import "errors"

// ErrNotFound is what anything that looks something up returns, wrapped, when
// it does not exist, so whoever asked can tell "no such thing" from a failure
// without matching message text — and without knowing where it was looked up.
var ErrNotFound = errors.New("not found")
