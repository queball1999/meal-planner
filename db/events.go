package db

import (
	"context"
	"fmt"
)

// LogEvent appends an entry to the audit log (§9.3). Errors are non-fatal to
// the calling request - callers may ignore the returned error for fire-and-forget
// logging, but it is returned so tests can assert on it.
func (s *store) LogEvent(ctx context.Context, e AppEvent) error {
	status := e.Status
	if status == "" {
		status = "ok"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO app_events
		     (actor_user_id, actor_label, action, target_type, target_id,
		      metadata, ip_address, user_agent, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullInt64(e.ActorUserID), e.ActorLabel, e.Action,
		nullStr(e.TargetType), nullStr(e.TargetID),
		nullStr(e.Metadata), e.IPAddress, e.UserAgent, status)
	if err != nil {
		return fmt.Errorf("log event %s: %w", e.Action, err)
	}
	return nil
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
