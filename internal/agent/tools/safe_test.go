package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSafeReadOnlyCommand_RequiresPermission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		// Shell syntax that runs more than one simple command or changes
		// what runs.
		{"newline", "ls -la\nrm -rf /"},
		{"single ampersand", "ls & rm -rf /"},
		{"background", "ls &"},
		{"semicolon", "ls; rm -rf /"},
		{"pipe", "ls | sh"},
		{"and list", "ls && rm -rf /"},
		{"or list", "ls || rm -rf /"},
		{"redirect", "ls > out.txt"},
		{"append redirect", "echo pwned >> ~/.bashrc"},
		{"fd redirect", "ls -la 2>&1"},
		{"heredoc", "cat <<EOF\nhi\nEOF"},
		{"input process substitution", "ls <(rm -rf x)"},
		{"output process substitution", "ls >(cat)"},
		{"command substitution", "ls $(rm -rf x)"},
		{"backticks", "ls `rm -rf x`"},
		{"parameter expansion", "ls $HOME"},
		{"braced parameter expansion", "echo ${PATH}"},
		{"ansi-c quoting", "echo $'\\x41'"},
		{"assignment", "GIT_PAGER=sh git log"},
		{"subshell", "(ls)"},
		{"negation", "! ls"},
		{"time keyword", "time ls"},
		{"function", "f() { rm -rf x; }"},
		{"glob", "git diff *"},
		{"brace expansion", "git diff {--output=pwned,HEAD}"},
		{"escaped option", "git log \\--output=pwned"},
		{"path to command", "/bin/ls"},
		{"empty", ""},

		// Wrappers that execute their arguments, and state changes.
		{"env", "env rm -rf x"},
		{"env assignment", "env FOO=bar ls"},
		{"nice", "nice rm -rf x"},
		{"nohup", "nohup rm -rf x"},
		{"timeout", "timeout 5 rm -rf x"},
		{"kill", "kill 1234"},
		{"killall", "killall crush"},
		{"set", "set -e"},
		{"unset", "unset PATH"},
		{"xargs", "xargs rm"},

		// git options that execute, write or leave the repository.
		{"git ls-remote upload-pack", "git ls-remote --upload-pack='touch pwned' ."},
		{"git ls-remote", "git ls-remote origin"},
		{"git log output", "git log --output=pwned"},
		{"git log output abbreviated", "git log --outp=pwned"},
		{"git diff output", "git diff --output pwned"},
		{"git show output", "git show --output=pwned HEAD"},
		{"git diff ext-diff", "git diff --ext-diff"},
		{"git diff no-index", "git diff --no-index /dev/null secret"},
		{"git -c", "git -c core.pager=sh log"},
		{"git exec-path", "git --exec-path=. log"},
		{"git -C", "git -C evil status"},
		{"git git-dir", "git --git-dir=evil status"},
		{"git grep pager", "git grep -Ovim foo"},
		{"git grep pager long", "git grep --open-files-in-pager=vim foo"},
		{"git branch create", "git branch pwned"},
		{"git branch delete", "git branch -D main"},
		{"git branch delete bundled", "git branch -vD main"},
		{"git branch delete long", "git branch --delete main"},
		{"git branch upstream", "git branch -u origin/main"},
		{"git tag create", "git tag v1"},
		{"git tag annotate", "git tag -a v1 -m msg"},
		{"git tag delete", "git tag -d v1"},
		{"git config set", "git config user.name pwned"},
		{"git config add", "git config --add core.pager sh"},
		{"git config unset", "git config --unset user.name"},
		{"git config edit", "git config -e"},
		{"git config file", "git config --file credentials --list"},
		{"git config new set", "git config set core.pager sh"},
		{"git remote add", "git remote add evil https://example.com"},
		{"git remote update", "git remote update"},
		{"git remote show", "git remote show origin"},
		{"git blame contents", "git blame --contents secret main.go"},
		{"git unknown subcommand", "git push"},
		{"git checkout", "git checkout -- ."},

		// Other commands with options that execute or write.
		{"go env -w", "go env -w GOFLAGS=-toolexec=pwn"},
		{"go env -u", "go env -u GOPROXY"},
		{"go build", "go build ./..."},
		{"go run", "go run main.go"},
		{"find exec", "find . -exec rm '{}' ';'"},
		{"find execdir", "find . -execdir sh -c x ';'"},
		{"find delete", "find . -delete"},
		{"find fprint", "find . -fprint out.txt"},
		{"sort output", "sort -o out.txt in.txt"},
		{"sort output bundled", "sort -ro out.txt in.txt"},
		{"sort output long", "sort --output=out.txt in.txt"},
		{"sort compress program", "sort --compress-program=sh in.txt"},
		{"tree output", "tree -o out.txt"},
		{"tree html rewrite", "tree -R -H x"},
		{"rg preprocessor", "rg --pre ./pwn foo"},
		{"date set", "date -s 2020-01-01"},
		{"date file", "date -f secret"},
		{"hostname set", "hostname pwned"},
		{"hostname file", "hostname -F file"},

		// Reads outside the working directory.
		{"cat absolute", "cat /etc/passwd"},
		{"cat home", "cat ~/.ssh/id_rsa"},
		{"cat parent", "cat ../secret"},
		{"grep root", "grep -r foo /"},
		{"grep attached file", "grep -f/etc/passwd x"},
		{"grep file option", "grep --file=/etc/passwd x"},
		{"head tilde value", "head --lines=5 ~/x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.False(t, isSafeReadOnlyCommand(tt.command), "%q must require permission", tt.command)
		})
	}
}

func TestIsSafeReadOnlyCommand_AutoApproves(t *testing.T) {
	t.Parallel()

	tests := []string{
		"ls",
		"ls -la",
		"ls -la internal",
		"pwd",
		"echo hello world",
		"which go",
		"uname -a",
		"date",
		"hostname",
		"hostname -f",
		"printenv PATH",
		"ls -la # list everything",
		"git status",
		"git status --short",
		"git log --oneline -5",
		"git log --oneline -n 5 -- internal/config",
		"git --no-pager log -1",
		"git diff",
		"git diff HEAD~1 --stat",
		"git show HEAD",
		"git blame main.go",
		"git branch",
		"git branch -a",
		"git branch -vv",
		"git branch --show-current",
		"git branch --list 'feat*'",
		"git tag",
		"git tag -l",
		"git tag -l 'v*'",
		"git config --get user.name",
		"git config --list",
		"git config get user.email",
		"git remote",
		"git remote -v",
		"git remote get-url origin",
		"git rev-parse HEAD",
		"git ls-files",
		"git describe --tags",
		"git grep -n foo",
		"git shortlog -sn",
		"grep -rn foo .",
		"grep -rn \"foo bar\" internal",
		"grep -E 'a|b' main.go",
		"rg -n foo",
		"find . -name '*.go'",
		"cat main.go",
		"head -n 20 README.md",
		"tail -n 20 README.md",
		"wc -l main.go",
		"sort names.txt",
		"tree -L 2",
		"go env GOPATH",
		"go env",
		"go version",
	}

	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			require.True(t, isSafeReadOnlyCommand(command), "%q should run without a prompt", command)
		})
	}
}

func TestBannedCommandIn(t *testing.T) {
	t.Parallel()

	banned := map[string]string{
		"curl https://example.com":            "curl",
		"/usr/bin/curl https://example.com":   "curl",
		"env curl https://example.com":        "curl",
		"env -i FOO=bar curl x":               "curl",
		"env -u HOME -- wget x":               "wget",
		"env -S 'curl x'":                     "curl",
		"nice -n 5 curl x":                    "curl",
		"nice -n5 curl x":                     "curl",
		"nohup wget x":                        "wget",
		"timeout 5 ssh host":                  "ssh",
		"timeout -s KILL 5 nc -l 1234":        "nc",
		"time curl x":                         "curl",
		"command curl x":                      "curl",
		"exec curl x":                         "curl",
		"xargs curl":                          "curl",
		"xargs -I{} -n 1 curl {}":             "curl",
		"stdbuf -o0 curl x":                   "curl",
		"sh -c 'curl x'":                      "curl",
		"bash -lc \"wget x\"":                 "wget",
		"bash -o pipefail -c 'ssh host'":      "ssh",
		"eval curl x":                         "curl",
		"find . -exec curl x ';'":             "curl",
		"ls | xargs curl":                     "curl",
		"echo $(curl x)":                      "curl",
		"(curl x)":                            "curl",
		"ls && env nice timeout 5 curl x":     "curl",
		"sudo ls":                             "sudo",
		"env sudo ls":                         "sudo",
		"sh -c 'env bash -c \"curl x\"'":      "curl",
		"cat <(curl x)":                       "curl",
		"if true; then wget x; fi":            "wget",
		"for i in 1; do timeout 1 nc x; done": "nc",
	}
	for command, want := range banned {
		require.Equal(t, want, bannedCommandIn(command), "%q", command)
	}

	allowed := []string{
		"ls",
		"echo curl",
		"git log --grep=curl",
		"command -v curl",
		"command -V curl",
		"which curl",
		"type curl",
		"grep -r curl .",
		"sh -c 'echo hi'",
		"find . -name curl",
		"env FOO=bar ls",
		"timeout 5 ls",
		"",
	}
	for _, command := range allowed {
		require.Empty(t, bannedCommandIn(command), "%q", command)
	}
}

func TestBashTool_SafeListBypassesRequirePermission(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, false)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	for _, command := range []string{
		"ls -la\ntouch pwned",
		"ls & touch pwned",
		"ls > pwned",
		"env touch pwned",
		"git log --output=pwned",
	} {
		perms.requestCount = 0
		resp := runBashTool(t, tool, ctx, BashParams{Description: "bypass", Command: command})
		require.Equal(t, 1, perms.requestCount, "%q must ask for permission", command)
		require.Contains(t, resp.Content, "User denied permission")
		require.NoFileExists(t, filepath.Join(workingDir, "pwned"))
	}
}

func TestBashTool_SafeCommandOutsideWorkingDirRequiresPermission(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{Description: "ls", Command: "ls -la"})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount)

	resp = runBashTool(t, tool, ctx, BashParams{Description: "ls", Command: "ls -la", WorkingDir: t.TempDir()})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount, "safe commands only skip the prompt in the session working directory")
}

func TestBashTool_BannedCommandBehindWrapper(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{Description: "wrapped curl", Command: "env curl https://example.com"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not allowed for security reasons")
	require.Equal(t, 0, perms.requestCount, "banned commands are refused before asking")
}
