package permission

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cmdParams struct{ cmd, dir string }

func (p cmdParams) TargetCommand() string { return p.cmd }
func (p cmdParams) TargetWorkdir() string { return p.dir }

func TestClassifyCommand(t *testing.T) {
	t.Parallel()
	cases := map[string]Class{
		"go test ./...":                      ClassWrite,
		"ls -la":                             ClassWrite,
		"rm -rf build":                       ClassDelete,
		"cd x && rm a.txt":                   ClassDelete,
		"sudo rm /tmp/x":                     ClassDelete,
		"find . -name '*.o' -delete":         ClassDelete,
		"git reset --hard HEAD~1":            ClassDelete,
		"git push --force origin main":       ClassDelete,
		"git clean -fdx":                     ClassDelete,
		"git status":                         ClassWrite,
		"git push origin main":               ClassNetwork,
		"curl https://example.com":           ClassNetwork,
		"npm install left-pad":               ClassNetwork,
		"go get example.com/x":               ClassNetwork,
		"echo format":                        ClassWrite,
		"grep -r remove .":                   ClassWrite,
		"psql -c 'DROP TABLE users'":         ClassDelete,
		"terraform plan":                     ClassWrite,
		"cat README | xargs rm":              ClassDelete,
		"go build -o bin/tada . && ./tada x": ClassWrite,
		// Deletes hidden behind a path, quotes, escapes or wrappers.
		"/bin/rm -rf build":             ClassDelete,
		`\rm x`:                         ClassDelete,
		`"rm" x`:                        ClassDelete,
		"'rm' x":                        ClassDelete,
		"env FOO=1 rm x":                ClassDelete,
		"nice -n 5 unlink x":            ClassDelete,
		"bash -c 'cd /tmp; rm -r x'":    ClassDelete,
		"echo $(rm x)":                  ClassDelete,
		"find . -exec /usr/bin/rm {} +": ClassDelete,
		"/usr/bin/curl example.com":     ClassNetwork,
		"python3 -c 'import shutil; shutil.rmtree(\"x\")'":         ClassDelete,
		"python -c \"import os; os.remove('a')\"":                  ClassDelete,
		"perl -e 'unlink glob q{*.txt}'":                           ClassDelete,
		"node -e \"require('fs').rmSync('x', {recursive: true})\"": ClassDelete,
		"ruby -e 'FileUtils.rm_rf(%q{x})'":                         ClassDelete,
		"python3.12 - <<'PY'\nimport os\nos.unlink('x')\nPY":       ClassDelete,
		"python3 -c 'import urllib.request'":                       ClassNetwork,
		"python3 -c 'print(1)'":                                    ClassWrite,
		"python3 script.py":                                        ClassWrite,
		// A script that doesn't parse can't be checked, so it asks.
		"echo 'unterminated": ClassDelete,
		"FOO=1":              ClassWrite,
		// Commands that touch protected project paths.
		"cat > .git/hooks/pre-commit <<'EOF'\nx\nEOF": ClassOutside,
		"cp evil.yml .github/workflows/ci.yml":        ClassOutside,
		"echo '{}' > tada.json":                       ClassOutside,
		"git config core.hooksPath hooks":             ClassOutside,
		"git -C sub config core.fsmonitor x":          ClassOutside,
		"git commit -m 'update config'":               ClassWrite,
		"cat .gitignore":                              ClassWrite,
		// Commands whose name is only known at run time can't be graded.
		"$X -rf build":                       ClassDelete,
		`"$(printf rm)" x`:                   ClassDelete,
		"a=rm; $a x":                         ClassDelete,
		"env $CMD x":                         ClassDelete,
		"command $CMD x":                     ClassDelete,
		"f() { \"$@\"; }; f rm x":            ClassDelete,
		"echo $HOME":                         ClassWrite,
		"{rm,-rf,build}":                     ClassDelete,
		"/bin/r? -rf build":                  ClassDelete,
		"[ -f x ] && echo y":                 ClassWrite,
		`awk 'BEGIN { system("rm -rf x") }'`: ClassDelete,
		`lua -e 'os.remove("x")'`:            ClassDelete,
		// Protected paths hidden behind quoting, globs or variables.
		"echo x > .gi''t/hooks/pre-commit":     ClassOutside,
		`cp hook ".git"/hooks/pre-commit`:      ClassOutside,
		"cp hook .g?t/hooks/pre-commit":        ClassOutside,
		"cp hook .GIT./hooks/pre-commit":       ClassOutside,
		"D=.git; cp hook $D/hooks/pre-commit":  ClassOutside,
		"echo x > $DIR/hooks/pre-commit":       ClassOutside,
		"git --git-dir=.gi''t log":             ClassOutside,
		"git -c x=y con''fig core.hooksPath h": ClassOutside,
		"cp a 'tad'a.json":                     ClassOutside,
		"ls *.go":                              ClassWrite,
		"cp hook .{g,x}it/hooks/pre-commit":    ClassOutside,
		"cp hook {.git,x}/hooks/pre-commit":    ClassOutside,
		"cp hook \"$(echo .gi)t/hooks/x\"":     ClassOutside,
		"cp hook $D/hooks/pre-commit":          ClassOutside,
		"git $SUB core.hooksPath h":            ClassOutside,
		"git -c core.fsmonitor=x status":       ClassOutside,
		"git --config-env=core.pager=X log":    ClassOutside,
		// Scripts run by sh -c and eval are checked like the outer one.
		`sh -c 'cp h $D/hooks/x'`:                 ClassOutside,
		`bash -c 'echo evil > $F'`:                ClassOutside,
		`sh -c 'cp h .g?t/hooks/x'`:               ClassOutside,
		`eval 'cp h $D/hooks/x'`:                  ClassOutside,
		`sudo sh -c "sh -c 'cp h \$D'"`:           ClassOutside,
		`sh -c "$SCRIPT"`:                         ClassOutside,
		`sh -c 'go test ./...'`:                   ClassWrite,
		`bash -c "cd internal && go build ./..."`: ClassWrite,
		// Git config set through the environment.
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.hooksPath GIT_CONFIG_VALUE_0=h git commit": ClassOutside,
		`env GIT_CONFIG_PARAMETERS="'core.hookspath'='h'" git commit`:                        ClassOutside,
		`export GIT_CONFIG_PARAMETERS="'core.hookspath'='h'"`:                                ClassOutside,
		`export GIT_"CONFIG_GLOBAL"=h`:                                                       ClassOutside,
		`export "$NAME"=h`:                                                                   ClassOutside,
		"GIT_DIR=/tmp/evil git status":                                                       ClassOutside,
		"GOOS=linux go build .":                                                              ClassWrite,
		"export PATH=$PATH:~/go/bin":                                                         ClassWrite,
		// A globbed git subcommand could be config.
		"git con?ig core.hooksPath h": ClassOutside,
		"git c*g core.hooksPath h":    ClassOutside,
		// Paths find and xargs only pick at run time.
		`find . -name pre-commit.sample -execdir cp evil pre-commit \;`: ClassOutside,
		`find . -name '*.go' -exec cp h {} \;`:                          ClassOutside,
		`printf '%s/hooks/x' "$D" | xargs cp h`:                         ClassOutside,
		`find . -name '*.go' -exec grep -l foo {} +`:                    ClassWrite,
		"git ls-files | xargs wc -l":                                    ClassWrite,
		"ls $DIR":                                                       ClassWrite,
		"git log --oneline":                                             ClassWrite,
		"cp a{1,2}.txt dst/":                                            ClassWrite,
		"echo hi > out.txt":                                             ClassWrite,
		// Read-only commands lose their exemption when the name may not be
		// the real program.
		"./cat $D/hooks/x":                                  ClassOutside,
		"PATH=.:$PATH cat $D/hooks/x":                       ClassOutside,
		"export PATH=./bin:$PATH; cat $D":                   ClassOutside,
		"shopt -s expand_aliases; alias cat='cp h'; cat $D": ClassOutside,
		"hash -p ./evil cat; cat $D":                        ClassOutside,
		"cat() { cp h \"$1\"; }; cat $D":                    ClassOutside,
		"env PATH=./bin cat $D":                             ClassOutside,
		// Variables named only at run time.
		`set -a; printf -v "$N" '%s' h; git commit`: ClassOutside,
		`declare -n r="$N"; r=h; git commit`:        ClassOutside,
		`printf '%s\n' "$x"`:                        ClassWrite,
		`printf -v out '%s' "$x"`:                   ClassWrite,
		// Git env that picks the repository or a program git runs.
		"GIT_SSH_COMMAND=./evil git status":     ClassOutside,
		"export GIT_PAGER=./evil":               ClassOutside,
		`GIT_EDITOR="$E" git commit`:            ClassOutside,
		"GIT_WORK_TREE=/tmp/x git status":       ClassOutside,
		"GIT_INDEX_FILE=/tmp/i git add .":       ClassOutside,
		"GIT_PAGER=cat git log":                 ClassWrite,
		"GIT_EDITOR=true git rebase --continue": ClassWrite,
		"GIT_TERMINAL_PROMPT=0 git status":      ClassWrite,
		"GIT_PAGER= git log":                    ClassWrite,
		// Git runs helper values through a shell, so only an exact
		// harmless value counts.
		`GIT_PAGER='cat $(cp h x)' git log`:   ClassOutside,
		`GIT_EDITOR='true;cp h x' git commit`: ClassOutside,
		"GIT_PAGER=less git log":              ClassOutside,
		// Shadowing through wrappers, names, nested scripts and sourcing.
		"builtin alias cat='cp h'; cat $D":                         ClassOutside,
		"command hash -p ./evil cat; cat $D":                       ClassOutside,
		"read PATH <<< ./bin; cat $D":                              ClassOutside,
		"printf -v PATH ./bin; cat $D":                             ClassOutside,
		"mapfile -t PATH < f; cat $D":                              ClassOutside,
		`eval 'alias cat="cp h"'; cat $D`:                          ClassOutside,
		". ./env.sh; cat $D":                                       ClassOutside,
		`env 'BASH_FUNC_cat%%=() { cp h "$1"; }' bash -c 'cat $D'`: ClassOutside,
		"BASH_ENV=./x bash -c 'cat $D'":                            ClassOutside,
		"echo $PATH; cat $D":                                       ClassWrite,
		// env options aren't assignments, even with an = in them.
		`env --split-string="sh -c 'cp h \$D'"`: ClassOutside,
		// The dynamic loader can load code into any program.
		"LD_PRELOAD=./evil.so cat $D":            ClassOutside,
		"DYLD_INSERT_LIBRARIES=./e.dylib cat $D": ClassOutside,
		// Code a trap runs later is graded now.
		`trap 'cp h "$D/hooks/x"' EXIT`:            ClassOutside,
		`trap -- 'cp h .git/hooks/x' EXIT`:         ClassOutside,
		"trap 'rm -f tmp.txt' EXIT; go test ./...": ClassDelete,
		"trap - EXIT":    ClassWrite,
		`trap "$X" EXIT`: ClassOutside,
		// Variables that make a shell or program run or load code.
		`PS4='$(cp h "$D")' SHELLOPTS=xtrace bash -c true`: ClassOutside,
		`export PS4='$(cp h "$D")'; set -x; true`:          ClassOutside,
		`PROMPT_COMMAND='cp h "$D"' bash -i`:               ClassOutside,
		"BASH_ENV=./x bash -c true":                        ClassOutside,
		"GCONV_PATH=./g iconv -f x":                        ClassOutside,
		"read PS4 < f; set -x":                             ClassOutside,
		"LD_LIBRARY_PATH=./lib ./build/app":                ClassWrite,
		"grep -rn ENV .":                                   ClassWrite,
		"echo $PS4":                                        ClassWrite,
		`printf -vPS4 '%s' '$(cp h "$D")'; set -x; true`:   ClassOutside,
		"read -aPS4 < f; set -x":                           ClassOutside,
		"printf -v 'PS4[0]' x; set -x":                     ClassOutside,
		`declare -n r=PS4; r='$(cp h "$D")'; set -x; true`: ClassOutside,
		"declare -n r; r=PS4; r=x; set -x":                 ClassOutside,
		"local -n r=GIT_DIR; r=/tmp/x; git status":         ClassOutside,
		"declare -n r=PATH; r=.; cat $D":                   ClassOutside,
		`PS1='$(cp h "$D")' bash -i`:                       ClassOutside,
		`mapfile -C 'cp h "$D"' -c 1 a < f`:                ClassOutside,
		"read MYVAR < f":                                   ClassWrite,
		"printf -v out '%s' x":                             ClassWrite,
		"declare -n r=out; r=1":                            ClassWrite,
		"mapfile -t lines < f":                             ClassWrite,
		`while read -r line; do echo "$line"; done < f`:    ClassWrite,
		"printf '%s\\n' PATH":                              ClassWrite,
	}
	for cmd, want := range cases {
		require.Equal(t, want, ClassifyCommand(cmd), cmd)
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cases := []struct {
		name string
		opts CreatePermissionRequest
		want Class
	}{
		{"budget", CreatePermissionRequest{ToolName: BudgetToolName}, ClassSpend},
		{"fetch", CreatePermissionRequest{ToolName: "fetch"}, ClassNetwork},
		{"view", CreatePermissionRequest{ToolName: "view"}, ClassRead},
		{"edit inside", CreatePermissionRequest{ToolName: "edit", Params: fileParams{"a.go"}}, ClassWrite},
		{"edit outside", CreatePermissionRequest{ToolName: "edit", Params: fileParams{"/etc/hosts"}}, ClassOutside},
		{"bash", CreatePermissionRequest{ToolName: "bash", Params: cmdParams{cmd: "go vet ./..."}}, ClassWrite},
		{"bash delete", CreatePermissionRequest{ToolName: "bash", Params: cmdParams{cmd: "rm x"}}, ClassDelete},
		{"bash outside", CreatePermissionRequest{ToolName: "bash", Params: cmdParams{cmd: "ls", dir: "/"}}, ClassOutside},
		{"bash outside delete", CreatePermissionRequest{ToolName: "bash", Params: cmdParams{cmd: "rm x", dir: "/"}}, ClassDelete},
		{"mcp delete", CreatePermissionRequest{ToolName: "mcp_github_delete_repo"}, ClassDelete},
		{"mcp send", CreatePermissionRequest{ToolName: "mcp_slack_send_message"}, ClassNetwork},
		{"mcp other", CreatePermissionRequest{ToolName: "mcp_db_query"}, ClassWrite},
		{"artifact delete", CreatePermissionRequest{ToolName: "artifact", Action: "delete"}, ClassDelete},
		{"machines add", CreatePermissionRequest{ToolName: "machines", Action: "add"}, ClassNetwork},
		{"machines remove", CreatePermissionRequest{ToolName: "machines", Action: "remove"}, ClassNetwork},
		{"edit git hook", CreatePermissionRequest{ToolName: "write", Params: fileParams{".git/hooks/pre-commit"}}, ClassOutside},
		{"edit workflow", CreatePermissionRequest{ToolName: "write", Params: fileParams{".github/workflows/ci.yml"}}, ClassOutside},
		{"edit project config", CreatePermissionRequest{ToolName: "edit", Params: fileParams{"sub/tada.json"}}, ClassOutside},
		{"edit tadarc", CreatePermissionRequest{ToolName: "edit", Params: fileParams{".tadarc"}}, ClassOutside},
		{"edit claude settings", CreatePermissionRequest{ToolName: "edit", Params: fileParams{".claude/settings.json"}}, ClassOutside},
		{"edit case trick", CreatePermissionRequest{ToolName: "edit", Params: fileParams{".GIT/config"}}, ClassOutside},
		{"edit trailing dot", CreatePermissionRequest{ToolName: "edit", Params: fileParams{"tada.json."}}, ClassOutside},
		{"edit gitignore", CreatePermissionRequest{ToolName: "edit", Params: fileParams{".gitignore"}}, ClassWrite},
		{"edit docs config", CreatePermissionRequest{ToolName: "edit", Params: fileParams{"docs/tada.json.md"}}, ClassWrite},
	}
	for _, c := range cases {
		require.Equal(t, c.want, Classify(c.opts, root), c.name)
	}
}

func TestClassHelpers(t *testing.T) {
	t.Parallel()
	require.Equal(t, ClassDelete, Stricter(ClassWrite, ClassDelete))
	require.Equal(t, ClassDelete, Stricter(ClassDelete, ClassRead))
	require.Equal(t, "edit", ClassWrite.Label())
	require.True(t, ClassSpend.Valid())
	require.False(t, Class("nope").Valid())
	p := DefaultPolicy()
	require.True(t, p.Asks(ClassDelete))
	require.True(t, p.Asks(ClassSpend))
	require.True(t, Policy{}.Asks(ClassSpend))
	require.False(t, p.Asks(ClassWrite))
}

func newControlService(t *testing.T) (*permissionService, string) {
	t.Helper()
	root := t.TempDir()
	return NewPermissionService(root, false, nil, nil).(*permissionService), root
}

// answer resolves the next published request.
func answer(t *testing.T, s *permissionService, grant bool) <-chan PermissionRequest {
	t.Helper()
	sub := s.Subscribe(t.Context())
	got := make(chan PermissionRequest, 1)
	go func() {
		ev := <-sub
		got <- ev.Payload
		if grant {
			s.Grant(ev.Payload)
		} else {
			s.Deny(ev.Payload)
		}
	}()
	return got
}

func TestAutoModeStillAsksForDelete(t *testing.T) {
	t.Parallel()
	s, _ := newControlService(t)
	s.SetSkipRequests(true)
	ctx := context.Background()

	ok, err := s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "bash", Params: cmdParams{cmd: "go test"}})
	require.NoError(t, err)
	require.True(t, ok)

	got := answer(t, s, false)
	ok, err = s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "bash", Params: cmdParams{cmd: "rm -rf x"}})
	require.NoError(t, err)
	require.False(t, ok)
	req := <-got
	require.Equal(t, ClassDelete, req.Class)
	require.Contains(t, req.Reason, "always ask")

	acts := s.Actions("s", 0)
	require.Len(t, acts, 2)
	require.Equal(t, DecisionDenied, acts[0].Decision)
	require.Equal(t, ViaUser, acts[0].Via)
	require.Equal(t, ViaAuto, acts[1].Via)
}

func TestEditsModeApprovesOnlyProjectEdits(t *testing.T) {
	t.Parallel()
	s, root := newControlService(t)
	s.SetEditsMode(true)
	ctx := context.Background()

	ok, err := s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "edit", Params: fileParams{filepath.Join(root, "a.go")}})
	require.NoError(t, err)
	require.True(t, ok)

	got := answer(t, s, true)
	ok, err = s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "bash", Params: cmdParams{cmd: "go test"}})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ClassWrite, (<-got).Class)

	got = answer(t, s, true)
	ok, err = s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "write", Params: fileParams{"/etc/x"}})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ClassOutside, (<-got).Class)

	// A symlink inside the project that points outside it is graded by
	// where it lands, so Edits mode asks instead of approving.
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link-out")))
	got = answer(t, s, true)
	ok, err = s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "edit", Params: fileParams{filepath.Join(root, "link-out", "x.go")}})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ClassOutside, (<-got).Class)

	// A git hook is inside the project but runs later, so it asks too.
	got = answer(t, s, true)
	ok, err = s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "write", Params: fileParams{filepath.Join(root, ".git", "hooks", "pre-commit")}})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ClassOutside, (<-got).Class)
}

func TestAutoBudget(t *testing.T) {
	t.Parallel()
	s, _ := newControlService(t)
	s.SetPolicy(Policy{AutoBudget: map[Class]int{ClassWrite: 2}, AlwaysAsk: []Class{"bogus"}})
	require.Empty(t, s.ControlState().Policy.AlwaysAsk)
	s.SetSkipRequests(true)
	ctx := context.Background()
	req := CreatePermissionRequest{SessionID: "s", ToolName: "bash", Params: cmdParams{cmd: "make"}}
	for range 2 {
		ok, err := s.Request(ctx, req)
		require.NoError(t, err)
		require.True(t, ok)
	}
	got := answer(t, s, true)
	ok, err := s.Request(ctx, req)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, (<-got).Reason, "used its 2 edit actions")

	// Answering refills the budget.
	ok, err = s.Request(ctx, req)
	require.NoError(t, err)
	require.True(t, ok)

	// Other sessions have their own budget.
	ok, err = s.Request(ctx, CreatePermissionRequest{SessionID: "other", ToolName: "bash", Params: cmdParams{cmd: "make"}})
	require.NoError(t, err)
	require.True(t, ok)
}

func TestTakeBack(t *testing.T) {
	t.Parallel()
	s, _ := newControlService(t)
	var (
		mu       sync.Mutex
		recorded []Action
	)
	s.SetRecorder(func(a Action) {
		mu.Lock()
		recorded = append(recorded, a)
		mu.Unlock()
	})
	ctx := context.Background()

	// Approve "bash" for the session.
	sub := s.Subscribe(ctx)
	go func() {
		ev := <-sub
		s.GrantPersistent(ev.Payload)
	}()
	req := CreatePermissionRequest{SessionID: "s", ToolName: "bash", Action: "execute", Path: ".", Params: cmdParams{cmd: "make"}}
	ok, err := s.Request(ctx, req)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.Request(ctx, req)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ViaSession, s.Actions("s", 1)[0].Via)

	s.SetSkipRequests(true)
	s.SetEditsMode(true)

	// Take back while a request waits.
	done := make(chan bool, 1)
	go func() {
		ok, _ := s.Request(ctx, CreatePermissionRequest{SessionID: "s", ToolName: "bash", Params: cmdParams{cmd: "rm x"}})
		done <- ok
	}()
	ev := <-sub
	require.Equal(t, ClassDelete, ev.Payload.Class)
	s.TakeBack()
	require.False(t, <-done)

	st := s.ControlState()
	require.False(t, st.Auto)
	require.False(t, st.Edits)
	require.False(t, st.TakenBackAt.IsZero())

	// The session approval is gone.
	go func() {
		ev := <-sub
		s.Deny(ev.Payload)
	}()
	ok, err = s.Request(ctx, req)
	require.NoError(t, err)
	require.False(t, ok)

	var sawTakeBack bool
	mu.Lock()
	defer mu.Unlock()
	for _, a := range recorded {
		if a.ToolName == TakeBackToolName {
			sawTakeBack = true
			require.Equal(t, DecisionEvent, a.Decision)
		}
	}
	require.True(t, sawTakeBack)

	// Turning a mode back on clears the taken-back mark.
	s.SetSkipRequests(true)
	require.True(t, s.ControlState().TakenBackAt.IsZero())
}

func TestHeadlessDeniesSpend(t *testing.T) {
	t.Parallel()
	s, _ := newControlService(t)
	s.AutoApproveSession("s")
	ok, err := s.Request(context.Background(), CreatePermissionRequest{SessionID: "s", ToolName: BudgetToolName})
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = s.Request(context.Background(), CreatePermissionRequest{SessionID: "s", ToolName: "bash", Params: cmdParams{cmd: "rm x"}})
	require.NoError(t, err)
	require.True(t, ok)
}

func TestAllowlistBeatsAlwaysAsk(t *testing.T) {
	t.Parallel()
	s := NewPermissionService(t.TempDir(), false, []string{"bash"}, nil).(*permissionService)
	ok, err := s.Request(context.Background(), CreatePermissionRequest{SessionID: "s", ToolName: "bash", Params: cmdParams{cmd: "rm x"}})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ViaAllowlist, s.Actions("", 1)[0].Via)
}

func TestTakenBackSince(t *testing.T) {
	t.Parallel()
	s, _ := newControlService(t)
	before := time.Now().Add(-time.Second)
	require.False(t, TakenBackSince(s, before))
	s.TakeBack()
	require.True(t, TakenBackSince(s, before))
	require.False(t, TakenBackSince(s, time.Now().Add(time.Hour)))
	require.False(t, TakenBackSince(nil, before))
}
