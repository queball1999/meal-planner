package homeassistant

import "errors"

// ErrNotConfigured is returned when a sync is attempted without a base URL and
// token. Callers surface it as a prompt to open the setup dialog.
var ErrNotConfigured = errors.New("home assistant is not configured")

var errNotConfigured = ErrNotConfigured
