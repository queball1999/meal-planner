package web

import (
	"sync/atomic"
	"time"
)

// appLoc is the APP_TIMEZONE every displayed timestamp is converted into.
// Package-level (not a Server field) so the template "local" func, which is
// built without a Server, can read it; atomic so a Settings save can swap it
// while requests are rendering.
var appLoc atomic.Pointer[time.Location]

// SetAppTimezone switches display timestamps to the named IANA zone. An
// unknown or empty name falls back to UTC.
func SetAppTimezone(name string) {
	loc, err := time.LoadLocation(name)
	if err != nil || name == "" {
		loc = time.UTC
	}
	appLoc.Store(loc)
}

// appTZName is the current app timezone's IANA name.
func appTZName() string {
	if loc := appLoc.Load(); loc != nil {
		return loc.String()
	}
	return "UTC"
}

// inAppTZ converts t to the app timezone for display. Zero times pass through
// unchanged so templates can keep testing .IsZero.
func inAppTZ(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	if loc := appLoc.Load(); loc != nil {
		return t.In(loc)
	}
	return t.UTC()
}
