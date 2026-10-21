package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Suggestion is a role the AI (or a rule of thumb) proposes for a
// machine, with the reason it fits.
type Suggestion struct {
	Name   string
	Role   string
	Reason string
}

// Roles a machine can be assigned by the rule of thumb.
const (
	roleCodeReview = "code review and tests"
	roleLongReads  = "long file reads and summaries"
	roleParallel   = "parallel search and bulk edits"
	roleLookups    = "quick lookups and drafts"
)

// suggestSystem tells the main AI how to assign roles.
const suggestSystem = `You help a lead coding agent divide side work among the user's local machines. Each machine runs one local model that can't use tools or see the repo; the lead agent sends it self-contained prompts. Given each machine's hardware, model, context window and slots, assign each one a short role (a few words, like "code review and tests") it is best at, with a one-sentence reason grounded in its resources. Answer with JSON only: an object mapping machine name to {"role": "...", "reason": "..."}. No prose, no code fences.`

// SuggestPrompt builds the system and user prompts that ask the main AI
// which role each machine suits. mainModel names the AI itself so it
// doesn't assign its own job away.
func SuggestPrompt(statuses []Status, mainModel string) (system, prompt string) {
	var b strings.Builder
	if mainModel != "" {
		fmt.Fprintf(&b, "The lead agent runs on %s and keeps planning, risky edits and final decisions.\n\n", mainModel)
	}
	b.WriteString("Machines:\n")
	for _, s := range statuses {
		fmt.Fprintf(&b, "- %s: %s", s.Name, describeForSuggest(s))
		b.WriteString("\n")
	}
	b.WriteString("\nAssign a role to every machine, even one that is offline. Reply with JSON only.")
	return suggestSystem, b.String()
}

func describeForSuggest(s Status) string {
	var parts []string
	if s.Hardware != "" {
		parts = append(parts, "hardware "+s.Hardware)
	}
	if s.Model != "" {
		parts = append(parts, "model "+s.Model)
	}
	if s.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("context window %d tokens", s.ContextWindow))
	}
	parts = append(parts, fmt.Sprintf("%d slot(s)", s.SlotCount()))
	if len(s.Tags) > 0 {
		parts = append(parts, "good for "+strings.Join(s.Tags, ", "))
	}
	if s.Role != "" {
		parts = append(parts, "current role "+s.Role)
	}
	if s.Notes != "" {
		parts = append(parts, "notes: "+s.Notes)
	}
	switch {
	case s.Disabled:
		parts = append(parts, "disabled")
	case s.Online:
		parts = append(parts, "online")
	default:
		parts = append(parts, "offline")
	}
	return strings.Join(parts, ", ")
}

// ParseSuggestions reads the AI's JSON answer, tolerating code fences
// and prose around it. Machines the answer doesn't name are left out;
// names it invents are dropped.
func ParseSuggestions(text string, statuses []Status) ([]Suggestion, error) {
	text = strings.TrimSpace(thinkBlock.ReplaceAllString(text, ""))
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, errors.New("the answer has no JSON object")
	}
	var raw map[string]struct {
		Role   string `json:"role"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return nil, fmt.Errorf("the answer isn't the expected JSON: %w", err)
	}
	var out []Suggestion
	for _, s := range statuses {
		r, ok := raw[s.Name]
		if !ok {
			for k, v := range raw {
				if strings.EqualFold(k, s.Name) {
					r, ok = v, true
					break
				}
			}
		}
		role := strings.TrimSpace(r.Role)
		if !ok || role == "" {
			continue
		}
		out = append(out, Suggestion{Name: s.Name, Role: role, Reason: strings.TrimSpace(r.Reason)})
	}
	if len(out) == 0 {
		return nil, errors.New("the answer names none of the machines")
	}
	return out, nil
}

// HeuristicSuggestions assigns roles from what each machine's resources
// imply, for when the AI can't be asked. Every machine gets one, and the
// list keeps the input order.
func HeuristicSuggestions(statuses []Status) []Suggestion {
	// Rank by context window so the biggest window gets the long reads
	// when several machines qualify.
	biggest := ""
	var biggestWindow int64
	for _, s := range statuses {
		if s.ContextWindow > biggestWindow && s.ContextWindow >= 65_536 {
			biggest, biggestWindow = s.Name, s.ContextWindow
		}
	}
	out := make([]Suggestion, 0, len(statuses))
	for _, s := range statuses {
		model := strings.ToLower(s.Model)
		var sug Suggestion
		switch {
		case strings.Contains(model, "coder") || strings.Contains(model, "code"):
			sug = Suggestion{Role: roleCodeReview, Reason: fmt.Sprintf("%s is a code model, so it reads diffs and drafts tests well.", s.Model)}
		case s.Name == biggest:
			sug = Suggestion{Role: roleLongReads, Reason: fmt.Sprintf("Its %d-token context window fits whole files that smaller windows can't.", s.ContextWindow)}
		case s.SlotCount() >= 4:
			sug = Suggestion{Role: roleParallel, Reason: fmt.Sprintf("With %d slots it can take several small jobs at once.", s.SlotCount())}
		default:
			sug = Suggestion{Role: roleLookups, Reason: "A single-slot general model is best kept to short, specific questions."}
		}
		sug.Name = s.Name
		switch {
		case s.Disabled:
			sug.Reason += " It is disabled, so enable it before it gets work."
		case !s.Online:
			sug.Reason += " It is offline right now."
		}
		out = append(out, sug)
	}
	return out
}

// Suggest asks the main AI for roles and falls back to the rule of thumb
// when it can't answer. fromAI reports which one produced the result;
// err is the AI's failure, if any, for the caller to show.
func Suggest(ctx context.Context, statuses []Status, mainModel string, ask func(ctx context.Context, system, prompt string) (string, error)) (sugs []Suggestion, fromAI bool, err error) {
	if len(statuses) == 0 {
		return nil, false, nil
	}
	if ask != nil {
		system, prompt := SuggestPrompt(statuses, mainModel)
		text, askErr := ask(ctx, system, prompt)
		if askErr == nil {
			sugs, askErr = ParseSuggestions(text, statuses)
		}
		if askErr == nil {
			return sugs, true, nil
		}
		err = askErr
	}
	return HeuristicSuggestions(statuses), false, err
}
