package tools

import (
	"context"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/control"
)

// MissionToolName is the name of the mission tool.
const MissionToolName = control.MissionToolName

// NewMissionTool returns the mission tool. It only changes the mission
// files in the project's data directory, so it never asks permission.
func NewMissionTool(missions *control.Missions) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MissionToolName,
		control.MissionToolDescription,
		func(ctx context.Context, params control.MissionParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			out, err := control.RunMissionTool(ctx, missions, GetSessionFromContext(ctx), params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(out), nil
		},
	)
}
