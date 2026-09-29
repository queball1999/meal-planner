package settings

import (
	"testing"
	"time"
)

// Every offered zone must load, or picking it would fail validation in Apply
// and silently keep the old zone.
func TestTimezonesResolve(t *testing.T) {
	seen := map[string]bool{}
	for _, tz := range Timezones {
		if seen[tz] {
			t.Errorf("%s listed twice", tz)
		}
		seen[tz] = true
		if _, err := time.LoadLocation(tz); err != nil {
			t.Errorf("%s: %v", tz, err)
		}
	}
}
