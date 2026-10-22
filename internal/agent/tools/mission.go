package tools

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/permission"
)

// MissionToolName is the name of the mission tool.
const MissionToolName = control.MissionToolName

// NewMissionTool returns the mission tool. It changes the mission files in
// the project's data directory without asking, except for setting a
// criterion's check command: that command later runs unattended in
// workingDir, so setting one asks permission the way the bash tool does.
func NewMissionTool(missions *control.Missions, permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MissionToolName,
		control.MissionToolDescription,
		func(ctx context.Context, params control.MissionParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			sessionID := GetSessionFromContext(ctx)
			if resp, ok, err := askMissionChecks(ctx, permissions, sessionID, call.ID, workingDir, params.Checks()); !ok || err != nil {
				return resp, err
			}
			out, err := control.RunMissionTool(ctx, missions, sessionID, params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(out), nil
		},
	)
}

// MissionCheckPermission describes check commands as the bash permission
// request that setting them needs: a check runs later without asking.
func MissionCheckPermission(checks []string, workingDir string) (description string, params BashPermissionsParams) {
	command := strings.Join(checks, "\n")
	description = "Set mission check: " + strings.Join(checks, "; ")
	return description, BashPermissionsParams{
		Description: description,
		Command:     command,
		WorkingDir:  workingDir,
	}
}

// askMissionChecks refuses banned check commands and asks permission for
// the rest. ok is false when the call must not go ahead; resp then says
// why.
func askMissionChecks(ctx context.Context, permissions permission.Service, sessionID, callID, workingDir string, checks []string) (fantasy.ToolResponse, bool, error) {
	if len(checks) == 0 {
		return fantasy.ToolResponse{}, true, nil
	}
	for _, c := range checks {
		if banned := BannedCommandIn(c); banned != "" {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("check command is not allowed for security reasons: %q", banned)), false, nil
		}
	}
	if permissions == nil {
		return fantasy.ToolResponse{}, true, nil
	}
	description, params := MissionCheckPermission(checks, workingDir)
	granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		Path:        workingDir,
		ToolCallID:  callID,
		ToolName:    BashToolName,
		Action:      "execute",
		Description: description,
		Params:      params,
	})
	if err != nil {
		return fantasy.ToolResponse{}, false, err
	}
	if !granted {
		return NewPermissionDeniedResponse(), false, nil
	}
	return fantasy.ToolResponse{}, true, nil
}
