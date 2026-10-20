package proto

import "github.com/charmbracelet/crush/internal/control"

// ControlResponse carries a control hub reply. A command the hub refused
// (for example completing a mission with open criteria) comes back with
// status 200 and the reason in Error, so clients can show it as is.
type ControlResponse struct {
	Reply control.Reply `json:"reply"`
	Error string        `json:"error,omitempty"`
}
