package tools

import (
	"context"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/permission"
)

// MachinesToolName is the name of the machines tool.
const MachinesToolName = fleet.ToolName

// NewMachinesTool returns the machines tool. Listing and checking are
// free; saving a machine asks first, because agents send code to whatever
// URL a machine has.
func NewMachinesTool(store fleet.Store, permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MachinesToolName,
		fleet.ToolDescription,
		func(ctx context.Context, params fleet.Params, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Changes() {
				ok, err := permissions.Request(ctx, permission.CreatePermissionRequest{
					SessionID:   GetSessionFromContext(ctx),
					Path:        workingDir,
					ToolCallID:  call.ID,
					ToolName:    MachinesToolName,
					Action:      strings.ToLower(strings.TrimSpace(params.Action)),
					Description: fleet.DescribeChange(params),
					Params:      params.Redacted(),
				})
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !ok {
					return NewPermissionDeniedResponse(), nil
				}
			}
			out, err := fleet.RunTool(ctx, store, nil, params)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(out), nil
		},
	)
}
