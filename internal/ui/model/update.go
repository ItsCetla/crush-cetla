package model

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/update"
	"github.com/charmbracelet/crush/internal/version"
)

// updateInfoTTL keeps update messages in the status bar a little longer
// than the default, since they tell the user what to do next.
const updateInfoTTL = 15 * time.Second

func updateInfo(t util.InfoType, msg string) util.InfoMsg {
	return util.InfoMsg{Type: t, Msg: msg, TTL: updateInfoTTL}
}

func updateStatePath() string {
	return filepath.Join(config.GlobalWorkspaceDir(), update.StateFile)
}

// checkForUpdatesCmd asks GitHub for the latest release (the palette's
// "Check for Updates"). It works even when the background check is off.
func checkForUpdatesCmd() tea.Cmd {
	return func() tea.Msg {
		current := version.Version
		if update.IsDevVersion(current) {
			return updateInfo(util.InfoTypeWarn, fmt.Sprintf("%s %s is a development build and doesn't update itself. Get a release from %s.", update.AppName, current, update.ReleasesPage))
		}
		res, err := update.New(current).Check(context.Background())
		if err != nil {
			return updateInfo(util.InfoTypeError, err.Error())
		}
		path := updateStatePath()
		s := update.LoadState(path)
		s.Record(res, time.Now())
		if res.Newer {
			// The user is about to read this; don't announce it again.
			s.NotifiedVersion = res.Latest
		}
		_ = update.SaveState(path, s)
		switch {
		case res.Release == nil:
			return updateInfo(util.InfoTypeInfo, "No "+update.AppName+" release has been published yet.")
		case res.Newer:
			return updateInfo(util.InfoTypeInfo, fmt.Sprintf("%s %s is out. Choose \"Install Update to %s\" in the command palette, or run `%s update`.", update.AppName, res.Latest, res.Latest, update.BinaryName))
		default:
			return updateInfo(util.InfoTypeSuccess, fmt.Sprintf("%s %s is up to date.", update.AppName, current))
		}
	}
}

// installUpdateCmd installs the latest release in place (the palette's
// "Install Update to X"). The Windows installer can't replace a running
// Ta-Da, so for that kind of install it points at `tada update` instead.
func installUpdateCmd(want string) tea.Cmd {
	current := version.Version
	exe, _ := update.RunningExecutable()
	inst := update.DetectInstall(current, runtime.GOOS, runtime.GOARCH, exe)
	switch inst.Kind {
	case update.KindDev:
		return util.CmdHandler(updateInfo(util.InfoTypeWarn, update.ErrDevBuild.Error()))
	case update.KindWindowsInstaller:
		return util.CmdHandler(updateInfo(util.InfoTypeInfo, fmt.Sprintf("Quit %s and run `%s update` to install %s.", update.AppName, update.BinaryName, want)))
	}
	install := func() tea.Msg {
		u := update.New(current)
		res, err := u.Check(context.Background())
		if err != nil {
			return updateInfo(util.InfoTypeError, err.Error())
		}
		if !res.Newer {
			return updateInfo(util.InfoTypeSuccess, fmt.Sprintf("%s %s is up to date.", update.AppName, current))
		}
		done, err := u.Install(context.Background(), res.Release, inst)
		if err != nil {
			var nw *update.ErrNotWritable
			if errors.As(err, &nw) {
				return updateInfo(util.InfoTypeError, fmt.Sprintf("Can't write to %s. Quit and run `sudo %s update`, or download it from %s.", nw.Dir, update.BinaryName, update.ReleasesPage))
			}
			return updateInfo(util.InfoTypeError, "Update failed: "+err.Error())
		}
		return updateInfo(util.InfoTypeSuccess, fmt.Sprintf("Updated to %s %s. Restart %s to use it.", update.AppName, done.Version, update.AppName))
	}
	return tea.Sequence(
		util.CmdHandler(updateInfo(util.InfoTypeInfo, fmt.Sprintf("Downloading %s %s…", update.AppName, want))),
		install,
	)
}
