package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/claudecode"
	"github.com/charmbracelet/crush/internal/question"
)

// Claude Code's AskUserQuestion tool is answered with Ta-Da's own
// question dialog. The CLI sends the questions as a permission prompt;
// Ta-Da asks the user and allows the call with the answers filled in,
// which is how the Agent SDK answers it too.

// ccAskInput is the AskUserQuestion input.
type ccAskInput struct {
	Questions []ccAskQuestion `json:"questions"`
}

type ccAskQuestion struct {
	Question    string        `json:"question"`
	Header      string        `json:"header"`
	Options     []ccAskOption `json:"options"`
	MultiSelect bool          `json:"multiSelect"`
}

type ccAskOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ccQuestionDescription is shown under a question when Claude Code gave
// no longer text for it; Ta-Da's dialog needs one.
const ccQuestionDescription = "Claude Code is asking."

// ccChoiceIDs returns the choice ID for each option: its label, made
// unique when two options share one. Ta-Da's card shows these IDs.
func ccChoiceIDs(q ccAskQuestion) []string {
	ids := make([]string, len(q.Options))
	seen := map[string]bool{}
	for i, o := range q.Options {
		id := ccClipRunes(o.Label, question.MaxChoiceLabelLength)
		if id == "" || seen[id] {
			id = fmt.Sprintf("%s (%d)", id, i+1)
		}
		seen[id] = true
		ids[i] = id
	}
	return ids
}

// ccQuestions turns AskUserQuestion input into Ta-Da questions.
func ccQuestions(in ccAskInput) []question.Question {
	out := make([]question.Question, 0, len(in.Questions))
	for _, q := range in.Questions {
		text := strings.TrimSpace(q.Question)
		desc := ccQuestionDescription
		if len(text) > question.MaxQuestionLength {
			desc = ccClipRunes(text, question.MaxDescriptionLength)
			text = ccClipRunes(text, question.MaxQuestionLength)
		}
		typ := question.TypeSingleChoice
		if q.MultiSelect {
			typ = question.TypeMultiChoice
		}
		var choices []question.Choice
		ids := ccChoiceIDs(q)
		for i, o := range q.Options {
			if i == question.MaxChoices {
				break
			}
			choices = append(choices, question.Choice{
				ID:          ids[i],
				Label:       ccClipRunes(o.Label, question.MaxChoiceLabelLength),
				Description: ccClipRunes(o.Description, question.MaxChoiceDescriptionLength),
			})
		}
		// Ta-Da's choice dialogs need two options; fewer means an open
		// question.
		if len(choices) < 2 {
			typ, choices = question.TypeFreeText, nil
		}
		out = append(out, question.Question{
			Type:        typ,
			Label:       q.Header,
			Text:        text,
			Description: desc,
			Choices:     choices,
		})
	}
	return out
}

// ccAnswerText is one answer in the form AskUserQuestion expects: the
// chosen labels joined by ", ", plus any typed text.
func ccAnswerText(q ccAskQuestion, a question.Answer) string {
	var parts []string
	ids := ccChoiceIDs(q)
	for _, sel := range a.SelectedIDs {
		for i, id := range ids {
			if id == sel {
				parts = append(parts, q.Options[i].Label)
				break
			}
		}
	}
	if t := strings.TrimSpace(a.FillInText); t != "" {
		parts = append(parts, t)
	}
	if a.Yes != nil {
		parts = append(parts, map[bool]string{true: "Yes", false: "No"}[*a.Yes])
	}
	return strings.Join(parts, ", ")
}

// ccAnswerNotes joins the notes the user attached to an answer.
func ccAnswerNotes(a question.Answer) string {
	var notes []string
	if n := strings.TrimSpace(a.Notes["_question"]); n != "" {
		notes = append(notes, n)
	}
	for k, n := range a.Notes {
		if k != "_question" && strings.TrimSpace(n) != "" {
			notes = append(notes, k+": "+strings.TrimSpace(n))
		}
	}
	return strings.Join(notes, "\n")
}

// ccAnswersInput builds the updated AskUserQuestion input carrying the
// user's answers, keyed by question text. Skipped questions are left out.
func ccAnswersInput(raw json.RawMessage, in ccAskInput, answers []question.Answer) (json.RawMessage, error) {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil || input == nil {
		input = map[string]any{}
	}
	answerMap := map[string]string{}
	annotations := map[string]any{}
	for i, a := range answers {
		if i >= len(in.Questions) {
			break
		}
		q := in.Questions[i]
		if t := ccAnswerText(q, a); t != "" {
			answerMap[q.Question] = t
		}
		if n := ccAnswerNotes(a); n != "" {
			annotations[q.Question] = map[string]string{"notes": n}
		}
	}
	input["answers"] = answerMap
	if len(annotations) > 0 {
		input["annotations"] = annotations
	}
	return json.Marshal(input)
}

// ccAskUser answers an AskUserQuestion prompt with Ta-Da's question
// dialog.
func (r *ccRun) ccAskUser(ctx context.Context, req claudecode.PermissionRequest) claudecode.Decision {
	if r.a.questions == nil {
		return claudecode.Decision{Message: "Interactive questions aren't available here. Ask the user in plain text and end your turn."}
	}
	var in ccAskInput
	if err := json.Unmarshal(req.Input, &in); err != nil || len(in.Questions) == 0 {
		return claudecode.Decision{Message: "AskUserQuestion needs at least one question."}
	}
	if len(in.Questions) > question.MaxQuestions {
		in.Questions = in.Questions[:question.MaxQuestions]
	}
	qs := ccQuestions(in)
	answers, err := r.a.questions.Ask(ctx, question.Request{
		SessionID:  r.call.SessionID,
		ToolCallID: req.ToolUseID,
		Questions:  qs,
	})
	if err != nil {
		if errors.Is(err, question.ErrCancelled) {
			r.questionReplies.Store(req.ToolUseID, "User cancelled this question")
			r.denied.Store(true)
			return claudecode.Decision{Message: "The user cancelled the question.", Interrupt: true}
		}
		return claudecode.Decision{Message: "Couldn't ask the user: " + err.Error() + ". Ask in plain text instead."}
	}
	updated, err := ccAnswersInput(req.Input, in, answers)
	if err != nil {
		return claudecode.Decision{Message: "Couldn't record the user's answers. Ask in plain text instead."}
	}
	// Show the answers on Ta-Da's question card in its own format.
	if resp, err := tools.FormatQuestionAnswers(answers, qs); err == nil {
		r.questionReplies.Store(req.ToolUseID, resp)
	}
	return claudecode.Decision{Allow: true, UpdatedInput: updated}
}

// ccAskToolInput rewrites AskUserQuestion input into the question tool's
// parameters so Ta-Da's question card can render it.
func ccAskToolInput(raw json.RawMessage) (string, bool) {
	var in ccAskInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", false
	}
	qs := ccQuestions(in)
	params := tools.QuestionParams{Questions: make([]tools.QuestionItem, len(qs))}
	for i, q := range qs {
		item := tools.QuestionItem{
			Label:       q.Label,
			Type:        string(q.Type),
			Question:    q.Text,
			Description: q.Description,
		}
		for _, c := range q.Choices {
			item.Choices = append(item.Choices, tools.QuestionChoice{ID: c.ID, Label: c.Label, Description: c.Description})
		}
		params.Questions[i] = item
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func ccClipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	for len(string(r)) > n-len("…") && len(r) > 0 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
