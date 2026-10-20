package tools

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/artifacts"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed artifact.md
var artifactDescription string

//go:embed artifact_data.md
var artifactDataDescription string

//go:embed artifact_comments.md
var artifactCommentsDescription string

const (
	ArtifactToolName         = "artifact"
	ArtifactDataToolName     = "artifact_data"
	ArtifactCommentsToolName = "artifact_comments"
)

// ArtifactToolNames lists the artifact tools.
var ArtifactToolNames = []string{ArtifactToolName, ArtifactDataToolName, ArtifactCommentsToolName}

// ArtifactResponseMetadata is attached to artifact tool results.
type ArtifactResponseMetadata = artifacts.Result

// schemaTool replaces a typed tool's generated schema with one that can
// describe free-form maps.
type schemaTool struct {
	fantasy.AgentTool
	props    map[string]any
	required []string
}

func (t schemaTool) Info() fantasy.ToolInfo {
	info := t.AgentTool.Info()
	info.Parameters = t.props
	info.Required = t.required
	return info
}

func withSchema[T any](t fantasy.AgentTool) fantasy.AgentTool {
	props, req := artifacts.ToolSchema[T]()
	return schemaTool{AgentTool: t, props: props, required: req}
}

// NewArtifactTools returns the artifact, artifact_data and
// artifact_comments tools.
func NewArtifactTools(svc *artifacts.Service, permissions permission.Service, workingDir string) []fantasy.AgentTool {
	env := func(ctx context.Context) artifacts.CallEnv {
		return artifacts.CallEnv{WorkingDir: workingDir, Project: workingDir, SessionID: GetSessionFromContext(ctx)}
	}

	artifact := fantasy.NewAgentTool(
		ArtifactToolName,
		artifactDescription,
		func(ctx context.Context, params artifacts.ArtifactParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			action := strings.ToLower(strings.TrimSpace(params.Action))
			sessionID := GetSessionFromContext(ctx)
			switch action {
			case "delete":
				id, err := artifacts.ParseID(params.ID)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				m, err := svc.Store.Get(id)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        workingDir,
					ToolCallID:  call.ID,
					ToolName:    ArtifactToolName,
					Action:      "delete",
					Description: fmt.Sprintf("Permanently delete the artifact %q (%s)", m.Title, id),
					Params:      params,
				})
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !granted {
					return NewPermissionDeniedResponse(), nil
				}
			case "", "publish":
				if outside := outsidePaths(workingDir, artifacts.SourcePaths(env(ctx), params)); len(outside) > 0 {
					granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
						SessionID:   sessionID,
						Path:        outside[0],
						ToolCallID:  call.ID,
						ToolName:    ArtifactToolName,
						Action:      "read",
						Description: "Publish files from outside the working directory: " + strings.Join(outside, ", "),
						Params:      params,
					})
					if err != nil {
						return fantasy.ToolResponse{}, err
					}
					if !granted {
						return NewPermissionDeniedResponse(), nil
					}
				}
			}
			res, err := svc.Artifact(ctx, env(ctx), params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(res.Text), *res), nil
		},
	)

	data := fantasy.NewAgentTool(
		ArtifactDataToolName,
		artifactDataDescription,
		func(ctx context.Context, params artifacts.DataParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			out, err := svc.Data(ctx, params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(out), nil
		},
	)

	comments := fantasy.NewAgentTool(
		ArtifactCommentsToolName,
		artifactCommentsDescription,
		func(ctx context.Context, params artifacts.CommentsParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			out, err := svc.Comments(ctx, params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(out), nil
		},
	)

	return []fantasy.AgentTool{
		withSchema[artifacts.ArtifactParams](artifact),
		withSchema[artifacts.DataParams](data),
		withSchema[artifacts.CommentsParams](comments),
	}
}

// outsidePaths returns the paths that aren't inside workingDir. It follows
// symlinks, so a link in the project that points elsewhere counts as
// outside.
func outsidePaths(workingDir string, paths []string) []string {
	wd, err := realPath(workingDir)
	if err != nil {
		return paths
	}
	var out []string
	for _, p := range paths {
		abs, err := realPath(p)
		if err != nil {
			out = append(out, p)
			continue
		}
		rel, err := filepath.Rel(wd, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out = append(out, abs)
		}
	}
	return out
}

// realPath is p made absolute with every symlink resolved. For a path that
// doesn't exist, it resolves the nearest parent that does.
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for dir := abs; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
