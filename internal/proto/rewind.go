package proto

import "github.com/charmbracelet/crush/internal/rewind"

// RewindRequest asks for a session to go back to just before a turn.
type RewindRequest = rewind.Request

// RewindResult describes a rewind, or the plan for one.
type RewindResult = rewind.Result

// RewindTurn is one turn of a session's timeline.
type RewindTurn = rewind.Turn

// SessionTimeline lists a session's turns, oldest first.
type SessionTimeline struct {
	Turns []RewindTurn `json:"turns"`
}

// ForkSessionRequest asks for a copy of a session.
type ForkSessionRequest struct {
	// AtMessageID is where the copy ends: a user message cuts just before
	// it, any other message after the end of its turn. Empty copies the
	// whole chat.
	AtMessageID string `json:"at_message_id,omitempty"`
}
