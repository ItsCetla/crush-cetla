package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// handleMachine implements the `machine` builtin.
//
// Usage:
//
//	machine add <name> --url URL [--model MODEL] [--slots N]
//	    [--hardware TEXT] [--type TYPE] [--api-key KEY]
//	    [--context-window N] [--tag TAG ...] [--role TEXT]
//	    [--use auto|named] [--notes TEXT] [--disabled true|false]
//	machine remove <name>   (alias: rm)
//
// "add" declares a computer on the network that serves models and how
// many agents it runs at once; repeated calls with the same <name> update
// the same entry. "remove" deletes it.
func handleMachine(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: machine add <name> --url URL [flags] | machine remove <name>")
	}

	switch args[1] {
	case "add":
		return machineAdd(b, args, stderr)
	case "remove", "rm":
		return machineRemove(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("machine: unknown subcommand %q (expected add or remove)", args[1]))
	}
}

// machineAddFlags is the declarative flag surface for `machine add`.
var machineAddFlags = []flagSpec{
	{name: "--url", jsonKey: "url", kind: flagString, op: opSet},
	{name: "--model", jsonKey: "model", kind: flagString, op: opSet},
	{name: "--slots", jsonKey: "slots", kind: flagInt, op: opSet},
	{name: "--hardware", jsonKey: "hardware", kind: flagString, op: opSet},
	{name: "--type", jsonKey: "type", kind: flagString, op: opSet},
	{name: "--api-key", jsonKey: "api_key", kind: flagString, op: opSet},
	{name: "--context-window", jsonKey: "context_window", kind: flagInt, op: opSet},
	{name: "--tag", jsonKey: "tags", kind: flagString, op: opAppend},
	{name: "--role", jsonKey: "role", kind: flagString, op: opSet},
	{name: "--use", jsonKey: "use", kind: flagString, op: opSet},
	{name: "--notes", jsonKey: "notes", kind: flagString, op: opSet},
	{name: "--disabled", jsonKey: "disabled", kind: flagBool, op: opSet},
}

func machineAdd(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: machine add <name> --url URL [--model MODEL] [--slots N] [--hardware TEXT] [--type TYPE] [--api-key KEY] [--context-window N] [--tag TAG ...] [--role TEXT] [--use auto|named] [--notes TEXT] [--disabled true|false]")
	}
	name := args[2]
	m := childMap(b.section("machines"), name)
	if err := applyFlags(machineAddFlags, args, 3, m, "machine add", stderr); err != nil {
		return err
	}
	slog.Debug("Machine defined in shell config", "name", name)
	return nil
}

func machineRemove(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: machine remove <name>")
	}
	name := args[2]
	delete(b.section("machines"), name)
	slog.Debug("Machine removed in shell config", "name", name)
	return nil
}
