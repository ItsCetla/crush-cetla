package permission

import (
	"slices"
	"time"

	"github.com/charmbracelet/crush/internal/pubsub"
)

var _ Controller = (*permissionService)(nil)

func (s *permissionService) currentPolicy() Policy {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.policy
}

// takeAuto spends one action of the session's auto budget for class. It
// returns false and the cap when the budget is used up.
func (s *permissionService) takeAuto(sessionID string, class Class, policy Policy) (bool, int) {
	limit, capped := policy.AutoBudget[class]
	if !capped || limit < 0 {
		return true, 0
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	used := s.autoUsed[sessionID]
	if used == nil {
		used = make(map[Class]int)
		s.autoUsed[sessionID] = used
	}
	if used[class] >= limit {
		return false, limit
	}
	used[class]++
	return true, 0
}

// resetAuto refills the auto budget of a class once the person has
// answered for it.
func (s *permissionService) resetAuto(sessionID string, class Class) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if used := s.autoUsed[sessionID]; used != nil {
		delete(used, class)
	}
}

func (s *permissionService) record(opts CreatePermissionRequest, class Class, decision Decision, via Via) {
	s.add(Action{
		At:          time.Now(),
		SessionID:   opts.SessionID,
		ToolCallID:  opts.ToolCallID,
		ToolName:    opts.ToolName,
		Action:      opts.Action,
		Description: opts.Description,
		Path:        opts.Path,
		Class:       class,
		Decision:    decision,
		Via:         via,
	})
}

func (s *permissionService) add(a Action) {
	s.controlMu.Lock()
	s.log = append(s.log, a)
	if over := len(s.log) - maxActions; over > 0 {
		s.log = slices.Delete(s.log, 0, over)
	}
	fn := s.recorder
	s.controlMu.Unlock()
	if fn != nil {
		fn(a)
	}
}

func (s *permissionService) SetEditsMode(on bool) {
	s.edits.Store(on)
	if on {
		s.controlMu.Lock()
		s.takenBackAt = time.Time{}
		s.controlMu.Unlock()
	}
}

func (s *permissionService) EditsMode() bool { return s.edits.Load() }

func (s *permissionService) TakeBack() {
	s.skip.Store(false)
	s.edits.Store(false)
	s.sessionPermissions.Reset(map[PermissionKey]bool{})

	now := time.Now()
	s.controlMu.Lock()
	s.autoUsed = make(map[string]map[Class]int)
	s.takenBackAt = now
	s.controlMu.Unlock()

	s.activeRequestMu.Lock()
	var active *PermissionRequest
	if s.activeRequest != nil {
		req := *s.activeRequest
		active = &req
	}
	s.activeRequestMu.Unlock()

	var sessionID string
	if active != nil {
		sessionID = active.SessionID
		s.resolve(*active, false, true, nil)
	}
	s.add(Action{
		At:          now,
		SessionID:   sessionID,
		ToolName:    TakeBackToolName,
		Description: "You took control back: Auto and Edits off, session approvals cleared.",
		Decision:    DecisionEvent,
		Via:         ViaTakeBack,
	})
	s.notificationBroker.Publish(pubsub.UpdatedEvent, PermissionNotification{
		SessionID: sessionID,
	})
}

func (s *permissionService) ControlState() ControlState {
	s.activeRequestMu.Lock()
	waiting := s.activeRequest != nil
	s.activeRequestMu.Unlock()
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	p := s.policy
	p.AlwaysAsk = slices.Clone(p.AlwaysAsk)
	return ControlState{
		Auto:        s.skip.Load(),
		Edits:       s.edits.Load(),
		TakenBackAt: s.takenBackAt,
		Waiting:     waiting,
		Policy:      p,
	}
}

func (s *permissionService) SetPolicy(p Policy) {
	p.AlwaysAsk = slices.DeleteFunc(slices.Clone(p.AlwaysAsk), func(c Class) bool { return !c.Valid() })
	s.controlMu.Lock()
	s.policy = p
	s.controlMu.Unlock()
}

func (s *permissionService) Actions(sessionID string, limit int) []Action {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	out := make([]Action, 0, min(len(s.log), max(limit, 0)))
	for i := len(s.log) - 1; i >= 0; i-- {
		if limit > 0 && len(out) >= limit {
			break
		}
		a := s.log[i]
		if sessionID != "" && a.SessionID != sessionID && a.SessionID != "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (s *permissionService) SetRecorder(fn func(Action)) {
	s.controlMu.Lock()
	s.recorder = fn
	s.controlMu.Unlock()
}
