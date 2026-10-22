package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

func TestConfineInput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, ".git"), filepath.Join(root, "sneaky")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "away")))

	const (
		isOutside = "outside this swarm's worktree"
		isGit     = "in git's files"
		isTwice   = "more than once"
		isString  = "must be a string"
		isInvalid = "isn't a valid JSON object"
	)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"relative inside", `{"file_path": "src/a.go"}`, ""},
		{"dot", `{"path": "."}`, ""},
		{"absolute inside", `{"path": "` + filepath.Join(root, "x") + `"}`, ""},
		{"null path", `{"path": null}`, ""},
		{"no path params", `{"command": "ls"}`, ""},
		{"gitignore is fine", `{"file_path": ".gitignore"}`, ""},
		{"parent", `{"file_path": "../x"}`, isOutside},
		{"sneaky parent", `{"path": "src/../../x"}`, isOutside},
		{"absolute outside", `{"file_path": "/etc/passwd"}`, isOutside},
		{"home", `{"path": "~/x"}`, isOutside},
		{"working dir", `{"command": "ls", "working_dir": "/"}`, isOutside},
		{"mixed case key", `{"File_Path": "/etc/passwd"}`, isOutside},
		{"upper case key", `{"WORKING_DIR": "/"}`, isOutside},
		{"kelvin sign key", `{"worKing_dir": "/"}`, isOutside},
		{"symlink out", `{"file_path": "away/x"}`, isOutside},
		{"git folder", `{"file_path": ".git/config"}`, isGit},
		{"git folder any case", `{"file_path": ".GIT/hooks/pre-commit"}`, isGit},
		{"nested git", `{"path": "sub/.git"}`, isGit},
		{"symlink to git", `{"file_path": "sneaky/config"}`, isGit},
		{"duplicate key", `{"file_path": "a.go", "file_path": "/etc/passwd"}`, isTwice},
		{"duplicate key in other case", `{"file_path": "a.go", "FILE_PATH": "/etc/passwd"}`, isTwice},
		{"non-string path", `{"path": 3}`, isString},
		{"object path", `{"file_path": {"x": 1}}`, isString},
		{"bad json", `{nope`, isInvalid},
		{"not an object", `["/etc/passwd"]`, isInvalid},
		{"trailing data", `{"path": "."} {"path": "/"}`, isInvalid},
		{"empty", ``, isInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := confineInput(root, tt.input)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestConfineInputJobs(t *testing.T) {
	t.Parallel()

	root, elsewhere := t.TempDir(), t.TempDir()
	mgr := shell.GetBackgroundShellManager()
	in, err := mgr.Start(t.Context(), root, nil, "sleep 10", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Kill(in.ID) })
	out, err := mgr.Start(t.Context(), elsewhere, nil, "sleep 10", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Kill(out.ID) })

	require.NoError(t, confineInput(root, `{"shell_id": "`+in.ID+`"}`))
	require.ErrorContains(t, confineInput(root, `{"shell_id": "`+out.ID+`"}`), "didn't start in this swarm's worktree")
	require.ErrorContains(t, confineInput(root, `{"Shell_ID": "`+out.ID+`"}`), "didn't start in this swarm's worktree")
	// An unknown job is left to the tool, which says it doesn't exist.
	require.NoError(t, confineInput(root, `{"shell_id": "nope"}`))
}

func TestKillWorktreeJobs(t *testing.T) {
	t.Parallel()

	root, elsewhere := t.TempDir(), t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	mgr := shell.GetBackgroundShellManager()
	in, err := mgr.Start(t.Context(), sub, nil, "sleep 10", "")
	require.NoError(t, err)
	out, err := mgr.Start(t.Context(), elsewhere, nil, "sleep 10", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Kill(out.ID) })

	require.Equal(t, 1, killWorktreeJobs(t.Context(), root))
	require.True(t, in.IsDone())
	require.False(t, out.IsDone())
}

type fakeToolInput struct {
	Path string `json:"path"`
}

func swarmFakeTool(name string, ran *[]string) fantasy.AgentTool {
	return fantasy.NewAgentTool(name, "fake", func(_ context.Context, in fakeToolInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		*ran = append(*ran, name+":"+in.Path)
		return fantasy.NewTextResponse("ok"), nil
	})
}

func TestConfineToolsFiltersAndConfines(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	var ran []string
	all := []fantasy.AgentTool{
		swarmFakeTool(tools.ViewToolName, &ran),
		swarmFakeTool(tools.WriteToolName, &ran),
		swarmFakeTool(tools.BashToolName, &ran),
		swarmFakeTool("agent", &ran),
		swarmFakeTool("fetch", &ran),
	}
	// Write isn't allowed for the coder, and agent and fetch aren't swarm
	// tools even though they are allowed.
	allowed := []string{tools.ViewToolName, tools.BashToolName, "agent", "fetch"}

	got := confineTools(all, allowed, root, nil)
	var names []string
	for _, tl := range got {
		names = append(names, tl.Info().Name)
		_, ok := tl.(*confinedTool)
		require.True(t, ok, tl.Info().Name)
	}
	require.Equal(t, []string{tools.ViewToolName, tools.BashToolName}, names)

	view := got[0]
	call := func(path string) fantasy.ToolResponse {
		in, err := json.Marshal(fakeToolInput{Path: path})
		require.NoError(t, err)
		resp, err := view.Run(t.Context(), fantasy.ToolCall{ID: "1", Name: tools.ViewToolName, Input: string(in)})
		require.NoError(t, err)
		return resp
	}

	resp := call("inside.txt")
	require.False(t, resp.IsError)
	require.Equal(t, []string{tools.ViewToolName + ":inside.txt"}, ran)

	resp = call("/etc/passwd")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "outside this swarm's worktree")
	require.Len(t, ran, 1, "the wrapped tool must not run")
}

func TestRunSwarmAgentRefusals(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	t.Run("no permission service", func(t *testing.T) {
		t.Parallel()
		c := &coordinator{}
		_, err := c.RunSwarmAgent(t.Context(), SwarmAgentRequest{WorkDir: dir})
		require.ErrorContains(t, err, "permission service")
	})

	t.Run("auto mode", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(dir, false, nil, nil)
		perms.SetSkipRequests(true)
		c := &coordinator{permissions: perms}
		_, err := c.RunSwarmAgent(t.Context(), SwarmAgentRequest{WorkDir: dir})
		require.ErrorIs(t, err, ErrSwarmAutoMode)
	})

	t.Run("relative worktree", func(t *testing.T) {
		t.Parallel()
		c := &coordinator{permissions: permission.NewPermissionService(dir, false, nil, nil)}
		_, err := c.RunSwarmAgent(t.Context(), SwarmAgentRequest{WorkDir: "rel/path"})
		require.ErrorContains(t, err, "isn't an absolute path")
		_, err = c.RunSwarmAgent(t.Context(), SwarmAgentRequest{})
		require.ErrorContains(t, err, "isn't an absolute path")
	})
}

func TestConfineGlobInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, ok := range []string{
		`{"pattern": "**/*.go"}`,
		`{"pattern": "internal/*.go", "path": "."}`,
		`{"pattern": "a..b/*.go"}`,
	} {
		require.NoError(t, confineGlobInput(root, ok), ok)
	}
	for _, bad := range []string{
		`{"pattern": "../../etc/*"}`,
		`{"pattern": "src/../../x"}`,
		`{"Pattern": "../*"}`,
		`{"pattern": "/etc/*"}`,
		`{"pattern": "~/.ssh/*"}`,
		`{"pattern": ".git/config"}`,
		`{"pattern": "**/.GIT/**"}`,
		`{"pattern": "..\\x"}`,
		`{"pattern": 5}`,
	} {
		require.Error(t, confineGlobInput(root, bad), bad)
	}
}
