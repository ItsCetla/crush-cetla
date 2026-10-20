package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/claudecode"
	"github.com/charmbracelet/crush/internal/claudecode/fakecli"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/stretchr/testify/require"
)

const ccAskInputJSON = `{"questions":[
	{"question":"Which database?","header":"DB","multiSelect":false,"options":[{"label":"Postgres","description":"Relational"},{"label":"SQLite","description":"File"}]},
	{"question":"Which extras?","header":"Extras","multiSelect":true,"options":[{"label":"Auth","description":""},{"label":"Docs","description":""},{"label":"CI","description":""}]}
]}`

// ccAnswerQuestions answers the next question batch with answer, or
// cancels it when answer is nil, and returns the request it saw.
func ccAnswerQuestions(t *testing.T, svc question.Service, answer func(question.Request) []question.Answer) <-chan question.Request {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	events := svc.Subscribe(ctx)
	seen := make(chan question.Request, 1)
	go func() {
		ev := <-events
		seen <- ev.Payload
		if answer == nil {
			svc.Cancel()
			return
		}
		svc.Answer(answer(ev.Payload))
	}()
	return seen
}

func TestClaudeCodeAskUserQuestionUsesQuestionDialog(t *testing.T) {
	t.Parallel()
	step1 := ccTextMessage(t, "m1", "Quick question.", "tool_use", [3]string{"AskUserQuestion", "tu-q", ccAskInputJSON})
	step1 = append(step1, ccPermRequest(t, "perm-q", "AskUserQuestion", "tu-q", ccAskInputJSON))
	step2 := []string{ccToolResult(t, "tu-q", `User has answered your questions: "Which database?"="SQLite"`, false, nil)}
	step2 = append(step2, ccTextMessage(t, "m2", "Using SQLite.", "end_turn")...)
	step2 = append(step2, ccResult(t, "Using SQLite.", false))

	env := newClaudeCodeTestAgent(t, true, false, step1, step2)
	svc := question.NewService()
	env.agent.questions = svc
	seen := ccAnswerQuestions(t, svc, func(req question.Request) []question.Answer {
		return []question.Answer{
			{QuestionID: req.Questions[0].ID, SelectedIDs: []string{"SQLite"}, Notes: map[string]string{"_question": "keep it simple"}},
			{QuestionID: req.Questions[1].ID, SelectedIDs: []string{"Auth", "CI"}, FillInText: "Lint"},
		}
	})
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)

	res, err := env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "set it up"})
	require.NoError(t, err)
	require.Equal(t, "Using SQLite.", res.Response.Content.Text())

	// The questions went to Ta-Da's dialog, not the permission dialog.
	req := <-seen
	require.Equal(t, sess.ID, req.SessionID)
	require.Equal(t, "tu-q", req.ToolCallID)
	require.Len(t, req.Questions, 2)
	require.Equal(t, question.TypeSingleChoice, req.Questions[0].Type)
	require.Equal(t, question.TypeMultiChoice, req.Questions[1].Type)
	require.Equal(t, "DB", req.Questions[0].Label)
	require.Equal(t, "Postgres", req.Questions[0].Choices[0].Label)
	require.Empty(t, env.perms.reqs)

	// The CLI got the answers the way AskUserQuestion expects them.
	in := fakecli.Input(t, env.dir)
	resp := in[len(in)-1]["response"].(map[string]any)["response"].(map[string]any)
	require.Equal(t, "allow", resp["behavior"])
	updated := resp["updatedInput"].(map[string]any)
	require.Equal(t, map[string]any{"Which database?": "SQLite", "Which extras?": "Auth, CI, Lint"}, updated["answers"])
	require.Equal(t, map[string]any{"Which database?": map[string]any{"notes": "keep it simple"}}, updated["annotations"])
	require.Len(t, updated["questions"], 2, "the original questions are kept")

	// The chat shows a question card with Ta-Da's answer text.
	msgs := ccMessages(t, env, sess.ID)
	require.Len(t, msgs, 4)
	calls := msgs[1].ToolCalls()
	require.Len(t, calls, 1)
	require.Equal(t, tools.QuestionToolName, calls[0].Name)
	var params tools.QuestionParams
	require.NoError(t, json.Unmarshal([]byte(calls[0].Input), &params))
	require.Len(t, params.Questions, 2)
	require.Equal(t, "Which database?", params.Questions[0].Question)
	result := msgs[2].ToolResults()[0]
	require.Equal(t, tools.QuestionToolName, result.Name)
	require.Contains(t, result.Content, "Q1: Which database?")
	require.Contains(t, result.Content, `User selected: ["SQLite"]`)
	require.Contains(t, result.Content, "User provided: Lint")
}

func TestClaudeCodeAskUserQuestionCancelEndsTurn(t *testing.T) {
	t.Parallel()
	step1 := ccTextMessage(t, "m1", "Quick question.", "tool_use", [3]string{"AskUserQuestion", "tu-q", ccAskInputJSON})
	step1 = append(step1, ccPermRequest(t, "perm-q", "AskUserQuestion", "tu-q", ccAskInputJSON))
	step2 := []string{
		ccToolResult(t, "tu-q", "The user cancelled the question.", true, nil),
		ccLine(t, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": "s"}),
	}
	env := newClaudeCodeTestAgent(t, true, false, step1, step2)
	svc := question.NewService()
	env.agent.questions = svc
	ccAnswerQuestions(t, svc, nil)
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)

	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "set it up"})
	require.NoError(t, err, "cancelling a question is not an error")

	in := fakecli.Input(t, env.dir)
	resp := in[len(in)-1]["response"].(map[string]any)["response"].(map[string]any)
	require.Equal(t, "deny", resp["behavior"])
	require.Equal(t, true, resp["interrupt"])
	msgs := ccMessages(t, env, sess.ID)
	require.Equal(t, "User cancelled this question", msgs[2].ToolResults()[0].Content)
}

func TestCCQuestionsEdgeCases(t *testing.T) {
	t.Parallel()
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	qs := ccQuestions(ccAskInput{Questions: []ccAskQuestion{
		{Question: string(long), Header: "Long", Options: []ccAskOption{{Label: "Same"}, {Label: "Same"}}},
		{Question: "Anything else?", Options: []ccAskOption{{Label: "Only one"}}},
	}})
	require.Len(t, qs, 2)
	require.NoError(t, question.Request{Questions: qs, ConfirmTitle: "t", ConfirmDescription: "d"}.Validate())
	require.LessOrEqual(t, len(qs[0].Text), question.MaxQuestionLength)
	require.Contains(t, qs[0].Description, "aaaa", "the full question moves to the description")
	require.NotEqual(t, qs[0].Choices[0].ID, qs[0].Choices[1].ID)
	require.Equal(t, question.TypeFreeText, qs[1].Type, "one option is an open question")

	// No question service: Claude Code is told to ask in plain text.
	r := &ccRun{a: &sessionAgent{}}
	d := r.canUseTool(t.Context(), claudecode.PermissionRequest{ToolName: "AskUserQuestion", Input: json.RawMessage(ccAskInputJSON)})
	require.False(t, d.Allow)
	require.Contains(t, d.Message, "plain text")
}
