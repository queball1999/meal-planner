package db

import (
	"context"
	"time"
)

// ChatMessage is one turn of the assistant conversation.
type ChatMessage struct {
	ID          int64
	HouseholdID int64
	Role        string // "user" | "assistant"
	Content     string
	AuditJSON   string // JSON array of the tool calls behind an assistant turn
	CreatedAt   time.Time
}

// AppendChatMessage records one turn.
func (s *store) AppendChatMessage(ctx context.Context, householdID int64, role, content, auditJSON string) (int64, error) {
	if auditJSON == "" {
		auditJSON = "[]"
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_messages (household_id, role, content, audit_json)
		VALUES (?, ?, ?, ?)`, householdID, role, content, auditJSON)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListChatMessages returns the most recent `limit` turns in chronological
// order.
//
// Newest-first internally, then reversed: a conversation is read oldest to
// newest, but what has to be *kept* when it grows long is the recent end.
func (s *store) ListChatMessages(ctx context.Context, householdID int64, limit int) ([]*ChatMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, role, content, audit_json, created_at
		FROM chat_messages
		WHERE household_id = ?
		ORDER BY id DESC
		LIMIT ?`, householdID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ChatMessage
	for rows.Next() {
		var m ChatMessage
		var created string
		if err := rows.Scan(&m.ID, &m.HouseholdID, &m.Role, &m.Content, &m.AuditJSON, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ClearChatMessages wipes a household's conversation.
func (s *store) ClearChatMessages(ctx context.Context, householdID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chat_messages WHERE household_id = ?`, householdID)
	return err
}
