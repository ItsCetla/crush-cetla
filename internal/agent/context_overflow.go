package agent

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"charm.land/fantasy"
)

// contextOverflowPhrases are lowercase fragments of the errors providers
// send when a prompt doesn't fit the model's context window. fantasy
// already recognises the big providers' wording; these cover the
// OpenAI-compatible servers (LM Studio, llama.cpp, vLLM, LiteLLM, …)
// that phrase it their own way.
var contextOverflowPhrases = []string{
	"maximum context length",
	"context_length_exceeded",
	"contextwindowexceeded",
	"context window exceeded",
	"prompt is too long",
	"exceeds the available context size",
	"exceeds the context window",
	"exceeds the maximum number of tokens",
	"input is too long",
	"context length of only",
	"reduce the length of the messages",
}

// contextWindowPatterns pull the window size out of an overflow error
// when the provider names it.
var contextWindowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)maximum context length is (\d+)`),
	regexp.MustCompile(`(?i)context length of (?:only )?(\d+)`),
	regexp.MustCompile(`(?i)context (?:window|size|length) (?:of|is) (\d+)`),
	regexp.MustCompile(`(?i)n_ctx(?:_slot)?\s*[=:]\s*(\d+)`),
}

// contextOverflow reports whether err is the provider refusing a prompt
// because it is larger than the model's context window. The second value
// is the window size the error named, or 0 when it didn't say.
func contextOverflow(err error) (bool, int64) {
	if err == nil || errors.Is(err, context.Canceled) {
		return false, 0
	}
	var texts []string
	var providerErr *fantasy.ProviderError
	if errors.As(err, &providerErr) {
		// A rate limit ("too many tokens per minute") is not an overflow.
		if providerErr.StatusCode == 429 {
			return false, 0
		}
		if providerErr.IsContextTooLarge() {
			if providerErr.ContextMaxTokens > 0 {
				return true, int64(providerErr.ContextMaxTokens)
			}
			return true, contextWindowFromText(providerErr.Message, string(providerErr.ResponseBody))
		}
		texts = append(texts, providerErr.Message, string(providerErr.ResponseBody))
	}
	texts = append(texts, err.Error())
	for _, text := range texts {
		lower := strings.ToLower(text)
		for _, phrase := range contextOverflowPhrases {
			if strings.Contains(lower, phrase) {
				return true, contextWindowFromText(texts...)
			}
		}
	}
	return false, 0
}

func contextWindowFromText(texts ...string) int64 {
	for _, text := range texts {
		for _, re := range contextWindowPatterns {
			if m := re.FindStringSubmatch(text); m != nil {
				if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 0
}

func contextWindowKey(m Model) string {
	return m.ModelCfg.Provider + "\x00" + m.ModelCfg.Model
}

// learnContextWindow remembers a window size a provider reported in an
// overflow error, for models whose catalog entry doesn't list one.
func (a *sessionAgent) learnContextWindow(m Model, window int64) {
	if window <= 0 {
		return
	}
	a.learnedWindows.Store(contextWindowKey(m), window)
}

// contextWindow is the model's context window: the catalog value, or
// the one learned from an earlier overflow error when the catalog has
// none. 0 means unknown.
func (a *sessionAgent) contextWindow(m Model) int64 {
	if cw := int64(m.CatwalkCfg.ContextWindow); cw > 0 {
		return cw
	}
	if v, ok := a.learnedWindows.Load(contextWindowKey(m)); ok {
		if cw, ok := v.(int64); ok {
			return cw
		}
	}
	return 0
}

const (
	// summaryToolResultLimit is how many characters of a tool result
	// survive when a summary request has to be shrunk to fit.
	summaryToolResultLimit = 4000
	// summaryTextLimit is the same cap for plain text parts.
	summaryTextLimit = 20000
	// summaryHistoryShare is the slice of the context window the
	// trimmed history may use, leaving room for the summary prompt and
	// the summary itself.
	summaryHistoryShare = 0.5
)

// trimForSummary shrinks a history that overflowed the context window so
// a summary request can fit in tokenBudget tokens. Long tool results and
// text are cut down to their start and end, attachments are dropped, and
// if that is not enough the oldest turns go. It returns the new history
// and whether any messages were dropped. msgs is not modified.
func trimForSummary(msgs []fantasy.Message, tokenBudget int64) ([]fantasy.Message, bool) {
	out := cloneFantasyMessages(msgs)
	for i := range out {
		parts := out[i].Content[:0]
		for _, part := range out[i].Content {
			if trimmed, keep := trimSummaryPart(part); keep {
				parts = append(parts, trimmed)
			}
		}
		out[i].Content = parts
	}

	// Leading system messages always stay.
	start := 0
	for start < len(out) && out[start].Role == fantasy.MessageRoleSystem {
		start++
	}
	dropped := false
	for tokenBudget > 0 && estimateMessageTokens(out) > tokenBudget {
		// Prefer dropping a whole earlier turn: cut up to the next user
		// message after the first one.
		if j := nextRole(out, start+1, fantasy.MessageRoleUser); j > 0 {
			out = append(out[:start:start], out[j:]...)
			dropped = true
			continue
		}
		// One long turn: drop its oldest assistant step together with
		// the tool results that answer it, keeping the user prompt.
		first := nextRole(out, start+1, fantasy.MessageRoleAssistant)
		if first < 0 {
			break
		}
		next := nextRole(out, first+1, fantasy.MessageRoleAssistant)
		if next < 0 {
			break
		}
		out = append(out[:first:first], out[next:]...)
		dropped = true
	}
	return out, dropped
}

func nextRole(msgs []fantasy.Message, from int, role fantasy.MessageRole) int {
	for i := from; i < len(msgs); i++ {
		if msgs[i].Role == role {
			return i
		}
	}
	return -1
}

func trimSummaryPart(part fantasy.MessagePart) (fantasy.MessagePart, bool) {
	switch p := part.(type) {
	case fantasy.TextPart:
		p.Text = truncateMiddle(p.Text, summaryTextLimit)
		return p, true
	case *fantasy.TextPart:
		c := *p
		c.Text = truncateMiddle(c.Text, summaryTextLimit)
		return &c, true
	case fantasy.FilePart, *fantasy.FilePart:
		return fantasy.TextPart{Text: "[attachment left out]"}, true
	case fantasy.ToolResultPart:
		p.Output = trimToolOutput(p.Output)
		return p, true
	case *fantasy.ToolResultPart:
		c := *p
		c.Output = trimToolOutput(c.Output)
		return &c, true
	default:
		return part, true
	}
}

func trimToolOutput(output fantasy.ToolResultOutputContent) fantasy.ToolResultOutputContent {
	switch o := output.(type) {
	case fantasy.ToolResultOutputContentText:
		o.Text = truncateMiddle(o.Text, summaryToolResultLimit)
		return o
	case *fantasy.ToolResultOutputContentText:
		return fantasy.ToolResultOutputContentText{Text: truncateMiddle(o.Text, summaryToolResultLimit)}
	case fantasy.ToolResultOutputContentMedia, *fantasy.ToolResultOutputContentMedia:
		return fantasy.ToolResultOutputContentText{Text: "[media output left out]"}
	default:
		return output
	}
}

// truncateMiddle keeps the first two thirds and the last third of a
// string longer than limit characters.
func truncateMiddle(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	head := strings.ToValidUTF8(s[:limit*2/3], "")
	tail := strings.ToValidUTF8(s[len(s)-limit/3:], "")
	return head + "\n\n[… " + strconv.Itoa(len(s)-len(head)-len(tail)) + " characters left out …]\n\n" + tail
}
