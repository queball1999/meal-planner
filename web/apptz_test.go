package web

import (
	"testing"
	"time"
)

func TestInAppTZ(t *testing.T) {
	t.Cleanup(func() { SetAppTimezone("UTC") })
	at := time.Date(2026, 9, 29, 3, 30, 0, 0, time.UTC)

	SetAppTimezone("America/Chicago")
	if got := inAppTZ(at).Format("Jan 2 15:04"); got != "Sep 28 22:30" {
		t.Errorf("Chicago = %q, want Sep 28 22:30", got)
	}

	SetAppTimezone("Not/A_Zone")
	if got := inAppTZ(at).Format("15:04"); got != "03:30" {
		t.Errorf("bad zone should fall back to UTC, got %q", got)
	}

	if !inAppTZ(time.Time{}).IsZero() {
		t.Error("zero time must stay zero")
	}
}
