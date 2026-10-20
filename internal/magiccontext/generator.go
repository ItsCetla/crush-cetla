// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): shared/subagent-runner.ts.

package magiccontext

import (
	"context"
	"errors"
)

// Generator runs one tool-less completion on a side model (historian,
// dreamer, smart-note evaluator). system is the system prompt, user the
// single user message. Implementations live in the agent package so this
// package stays free of provider code.
type Generator func(ctx context.Context, system, user string, maxTokens int) (string, error)

// Embedder turns texts into vectors. nil means embeddings are off.
type Embedder func(ctx context.Context, texts []string) ([][]float32, error)

// ErrNoGenerator is returned when a side-model task has no model configured.
var ErrNoGenerator = errors.New("magic context: no side model configured")
