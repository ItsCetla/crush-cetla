package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/config"
)

// AskToolName is the name of the tool that sends one prompt to a machine.
const AskToolName = "machine_ask"

// AskToolDescription tells the model what machine_ask does.
const AskToolDescription = `Send one self-contained prompt to a local model on one of the user's machines and get its answer back. Use it to offload side work in parallel: summarizing files, reviewing a file for bugs, drafting tests, docs or boilerplate, explaining code. The machine's model can't use tools or see the repo, so put everything it needs in the prompt, or list repo files in files to include them. It is weaker than you: keep tasks small and specific, and check what it returns before using it. Leave machine empty to pick the machine with the most free slots. It fails when every slot is busy; then do the work yourself.`

// AskParams are machine_ask's arguments.
type AskParams struct {
	Prompt  string   `json:"prompt" description:"The task, with all the context the model needs" jsonschema:"The task, with all the context the model needs"`
	Machine string   `json:"machine,omitempty" description:"Machine name; empty or auto picks the one with the most free slots" jsonschema:"Machine name; empty or auto picks the one with the most free slots"`
	Files   []string `json:"files,omitempty" description:"Repo files to include, relative to the working directory" jsonschema:"Repo files to include, relative to the working directory"`
}

const (
	askTimeout = 10 * time.Minute
	// About 3 characters per token, keeping a third of the window free
	// for the system prompt and the answer.
	charsPerToken    = 3
	defaultAskWindow = 32768
)

const askSystemPrompt = "You are a careful coding assistant helping a more capable lead agent. Do exactly the task you are given, using only the context provided. Be concise and concrete. If the context isn't enough to answer, say what is missing instead of guessing."

// Ask runs one prompt on a machine, holding one of its slots while the
// model answers. workingDir bounds which files can be included.
func Ask(ctx context.Context, cfg *config.Config, pool *Pool, workingDir string, p AskParams) (string, error) {
	if strings.TrimSpace(p.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	if pool == nil {
		pool = Default()
	}
	lease, err := pool.Acquire(ctx, cfg, strings.TrimSpace(p.Machine))
	if errors.Is(err, ErrNoneFree) {
		return "", errors.New("every machine slot is busy or no machine is reachable; do this yourself or try again later")
	}
	if err != nil {
		return "", err
	}
	defer lease.Release()
	m := lease.Machine

	window := m.ContextWindow
	if window <= 0 {
		window = defaultAskWindow
	}
	budget := int(window) * charsPerToken * 2 / 3
	prompt, err := withFiles(p.Prompt, p.Files, workingDir, budget)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	answer, err := chat(ctx, m, prompt)
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.Name, err)
	}
	return fmt.Sprintf("[ran on %s (%s)]\n%s", m.Name, m.Model, answer), nil
}

// withFiles appends the named files to prompt, keeping the total within
// budget characters. Files must be inside workingDir.
func withFiles(prompt string, files []string, workingDir string, budget int) (string, error) {
	if len(files) == 0 {
		return prompt, nil
	}
	root, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	var b strings.Builder
	b.WriteString(prompt)
	left := budget - len(prompt)
	for _, name := range files {
		path, err := inside(root, name)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			return "", fmt.Errorf("%s isn't a text file", name)
		}
		rel, _ := filepath.Rel(root, path)
		header := fmt.Sprintf("\n\n--- %s ---\n", filepath.ToSlash(rel))
		if left <= len(header) {
			return "", errors.New("the files don't fit in the machine's context window; send fewer or smaller files")
		}
		left -= len(header)
		b.WriteString(header)
		text := string(data)
		if len(text) > left {
			return "", fmt.Errorf("%s doesn't fit in the machine's context window; send part of it in the prompt instead", name)
		}
		left -= len(text)
		b.WriteString(text)
	}
	return b.String(), nil
}

// inside resolves name against root and refuses anything that lands
// outside it, symlinks included.
func inside(root, name string) (string, error) {
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is outside the working directory", name)
	}
	return path, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// chat makes one non-streaming chat completion call to m.
func chat(ctx context.Context, m Machine, prompt string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: m.Model,
		Messages: []chatMessage{
			{Role: "system", Content: askSystemPrompt},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req, m)
	resp, err := machineClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		if resp.StatusCode >= 300 {
			return "", fmt.Errorf("server answered %s", resp.Status)
		}
		return "", fmt.Errorf("unreadable answer: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", errors.New(out.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("server answered %s", resp.Status)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("the model returned no answer")
	}
	answer := strings.TrimSpace(thinkBlock.ReplaceAllString(out.Choices[0].Message.Content, ""))
	if answer == "" {
		return "", errors.New("the model returned an empty answer")
	}
	return answer, nil
}

func setAuth(req *http.Request, m Machine) {
	if key := m.APIKey; key != "" && !strings.HasPrefix(key, "$") {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
