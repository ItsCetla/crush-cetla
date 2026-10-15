package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/require"
)

func TestLoadFromSource_NonExistentDir(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "does-not-exist")

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Empty(t, cmds)

	// directory must NOT have been created
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr))
}

func TestLoadFromSource_ExistingDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("say hello"), 0o644))

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "user:hello", cmds[0].ID)
	require.Equal(t, "say hello", cmds[0].Content)
}

func TestLoadAll_MixedSources(t *testing.T) {
	t.Parallel()

	existing := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(existing, "cmd.md"), []byte("content"), 0o644))

	missing := filepath.Join(t.TempDir(), "nope")

	cmds, err := loadAll([]commandSource{
		{path: existing, prefix: userCommandPrefix},
		{path: missing, prefix: projectCommandPrefix},
	})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "user:cmd", cmds[0].ID)
}

func TestFromSkillCatalog_UserInvocableOnly(t *testing.T) {
	t.Parallel()

	cmds := FromSkillCatalog([]skills.CatalogEntry{
		{
			ID:            "/skills/on/SKILL.md",
			Name:          "on",
			Description:   "Enabled.",
			Label:         "user:on",
			UserInvocable: true,
		},
		{
			ID:            "/skills/off/SKILL.md",
			Name:          "off",
			Description:   "Not invocable.",
			Label:         "user:off",
			UserInvocable: false,
		},
	})

	require.Len(t, cmds, 1)
	require.Equal(t, "user:on", cmds[0].ID)
	require.Equal(t, "user:on", cmds[0].Name)
	require.Equal(t, "on", cmds[0].Skill.Name)
	require.Equal(t, "Enabled.", cmds[0].Skill.Description)
	require.Equal(t, "/skills/on/SKILL.md", cmds[0].Skill.SkillFilePath)
}

func TestFromSkillCatalog_UsesDiscoveredSymlinkedSkills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires special privileges on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	targetParent := t.TempDir()
	targetSkillDir := filepath.Join(targetParent, "linked-skill")
	require.NoError(t, os.MkdirAll(targetSkillDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(targetSkillDir, skills.SkillFileName),
		[]byte("---\nname: linked-skill\ndescription: Symlinked.\nuser-invocable: true\n---\nUse me.\n"),
		0o644,
	))

	link := filepath.Join(root, "linked-skill")
	require.NoError(t, os.Symlink(targetSkillDir, link))

	_, activeSkills, _ := skills.DiscoverFromConfig(skills.DiscoveryConfig{
		SkillsPaths: []string{root},
	})
	entries := skills.Catalog(activeSkills, []string{root}, "")
	cmds := FromSkillCatalog(entries)

	require.Len(t, cmds, 1)
	require.Equal(t, "user:linked-skill", cmds[0].ID)
	require.Equal(t, "linked-skill", cmds[0].Skill.Name)
	require.Equal(t, filepath.Join(link, skills.SkillFileName), cmds[0].Skill.SkillFilePath)
}

func TestFromOpenCode(t *testing.T) {
	t.Parallel()

	cmds := FromOpenCode([]config.OpenCodeCommand{
		{Name: "git/push", Description: "Push the branch", Template: "Push to $1 as $2, then confirm $1."},
		{Name: "review", Template: "Review $ARGUMENTS"},
	})
	require.Len(t, cmds, 2)

	push := cmds[0]
	require.Equal(t, "opencode:git:push", push.ID)
	require.Equal(t, push.ID, push.Name)
	require.Equal(t, "Push the branch", push.Description)
	require.Equal(t, "Push to $ARG1 as $ARG2, then confirm $ARG1.", push.Content)
	require.Equal(t, []Argument{
		{ID: "ARG1", Title: "ARG1"},
		{ID: "ARG2", Title: "ARG2"},
	}, push.Arguments)
	require.Nil(t, push.Skill)

	review := cmds[1]
	require.Equal(t, "opencode:review", review.ID)
	require.Equal(t, "Review $ARGUMENTS", review.Content)
	require.Equal(t, []Argument{{ID: "ARGUMENTS", Title: "ARGUMENTS"}}, review.Arguments)
}

func TestOpenCodeTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		content  string
		args     []string
	}{
		{"no placeholders", "Run the tests.", "Run the tests.", nil},
		{"zero stays literal", "Cost is $0 today.", "Cost is $0 today.", nil},
		{"multi-digit stays literal", "Pay $10 for $1.", "Pay $10 for $ARG1.", []string{"ARG1"}},
		{"first-seen order", "$2 then $1 then $2", "$ARG2 then $ARG1 then $ARG2", []string{"ARG2", "ARG1"}},
		{"arguments and positional", "$1: $ARGUMENTS", "$ARG1: $ARGUMENTS", []string{"ARG1", "ARGUMENTS"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content, args := openCodeTemplate(tt.template)
			require.Equal(t, tt.content, content)
			var ids []string
			for _, a := range args {
				require.Equal(t, a.ID, a.Title)
				require.False(t, a.Required)
				ids = append(ids, a.ID)
			}
			require.Equal(t, tt.args, ids)
		})
	}
}
