package db

import (
	"context"
	"fmt"
	"time"
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

// AppEventRow is one row read back from the audit log - AppEvent plus the two
// columns only the reader needs (LogEvent never has to set them).
type AppEventRow struct {
	ID         int64
	OccurredAt time.Time
	AppEvent
}

// ListEvents returns the most recent audit-log entries, newest first, capped
// at limit (defaulting to 200, capped at 1000 - this is the Settings →
// "Admin events" viewer, not an export; ListEvents in Go and paginate() in
// web/pagination.go slice it for display the same way ListPlans/handlePlan-
// History already do for plan history).
func (s *store) ListEvents(ctx context.Context, limit int) ([]*AppEventRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, occurred_at, actor_user_id, actor_label, action, target_type,
		       target_id, metadata, ip_address, user_agent, status
		FROM app_events ORDER BY occurred_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var out []*AppEventRow
	for rows.Next() {
		var e AppEventRow
		var occurredAt string
		var actorUserID *int64
		var targetType, targetID, metadata, ipAddress, userAgent *string
		if err := rows.Scan(&e.ID, &occurredAt, &actorUserID, &e.ActorLabel, &e.Action,
			&targetType, &targetID, &metadata, &ipAddress, &userAgent, &e.Status); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurredAt)
		e.ActorUserID = actorUserID
		if targetType != nil {
			e.TargetType = *targetType
		}
		if targetID != nil {
			e.TargetID = *targetID
		}
		if metadata != nil {
			e.Metadata = *metadata
		}
		if ipAddress != nil {
			e.IPAddress = *ipAddress
		}
		if userAgent != nil {
			e.UserAgent = *userAgent
		}
		out = append(out, &e)
	}
	return out, rows.Err()
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
