// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): packages/plugin/src/features/magic-context/message-index.ts (getIndexableContent, ensureMessagesIndexed)

package magiccontext

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/message"
)

// messageIndexMaxBytes bounds one indexed document, like upstream's FTS cap.
const messageIndexMaxBytes = 64 * 1024

// IndexableContent is the searchable text of a raw message: cleaned user
// text or assistant text without system reminders, parts joined by " / ".
// Tool messages and hidden or empty user turns index nothing.
func IndexableContent(m RawMessage) string {
	switch m.Role {
	case message.User:
		if m.Hidden || !m.MeaningfulUser() {
			return ""
		}
	case message.Assistant:
	default:
		return ""
	}
	var parts []string
	for _, t := range m.Texts {
		if t = normalizeText(removeSystemReminders(t)); t != "" {
			parts = append(parts, t)
		}
	}
	out := strings.Join(parts, " / ")
	if len(out) > messageIndexMaxBytes {
		cut := messageIndexMaxBytes
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut]
	}
	return out
}

// DeleteMessageIndex removes indexed rows by ordinal.
func (s *Store) DeleteMessageIndex(ctx context.Context, sessionID string, ordinals []int) error {
	if len(ordinals) == 0 {
		return nil
	}
	return s.Immediate(ctx, func(q querier) error {
		for _, o := range ordinals {
			if _, err := q.ExecContext(ctx, `DELETE FROM mc_message_index WHERE session_id = ? AND ordinal = ?`, sessionID, o); err != nil {
				return err
			}
		}
		return nil
	})
}

// syncMessageIndex brings mc_message_index in line with the live raw view:
// new or changed messages are upserted and rows whose message is gone or no
// longer indexable are removed. Length is the change signal; a same-length
// edit with a new message id is caught by the id check.
func (e *Engine) syncMessageIndex(ctx context.Context, sid string, raw []RawMessage) error {
	have, err := e.Store.IndexedLengths(ctx, sid)
	if err != nil {
		return err
	}
	ids, err := e.Store.indexedIDs(ctx, sid)
	if err != nil {
		return err
	}
	live := make(map[int]bool, len(raw))
	var up []IndexedMessage
	var del []int
	for _, m := range raw {
		c := IndexableContent(m)
		n, ok := have[m.Ordinal]
		if c == "" {
			if ok {
				del = append(del, m.Ordinal)
			}
			continue
		}
		live[m.Ordinal] = true
		if ok && n == utf8.RuneCountInString(c) && ids[m.Ordinal] == m.ID {
			continue
		}
		up = append(up, IndexedMessage{Ordinal: m.Ordinal, MessageID: m.ID, Role: string(m.Role), Content: c, CreatedAt: m.CreatedAt * 1000})
	}
	// Rows above the live view belong to messages deleted from the session.
	// Rows below it are kept: pruned history stays searchable as an archive.
	last := 0
	if len(raw) > 0 {
		last = raw[len(raw)-1].Ordinal
	}
	for o := range have {
		if o > last && !live[o] {
			del = append(del, o)
		}
	}
	if err := e.Store.DeleteMessageIndex(ctx, sid, del); err != nil {
		return err
	}
	return e.Store.UpsertMessageIndex(ctx, sid, up)
}

func (s *Store) indexedIDs(ctx context.Context, sessionID string) (map[int]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal, message_id FROM mc_message_index WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var o int
		var id string
		if err := rows.Scan(&o, &id); err != nil {
			return nil, err
		}
		out[o] = id
	}
	return out, rows.Err()
}
