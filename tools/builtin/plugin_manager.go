package builtintools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qiyuan2403/Q_RP_dsh/internal/pluginmgr"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
)

// pluginManagerTool manages the npm plugin profile: list what is installed,
// install a package from the npm registry, remove one. It is registered only
// when Options.ProfileDir is set — without a profile there is nothing to
// manage, and a tool that always errors is noise in every turn's schema.
//
// Installs run on the caller's context; the registry bounds them by the
// pluginmgr's own HTTP timeout, not the tool timeout, because a cold npm
// resolve can legitimately take longer than a shell command.
func pluginManagerTool(cfg Options, hostVersion string) tools.Tool {
	return tools.Tool{
		Name: "plugin_manager",
		Description: "Manage installed npm plugins for the JS evaluator. " +
			"Installed packages are importable by name from !!js expressions. " +
			"Registry packages must be pure JavaScript (no native builds).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["list", "install", "remove"], "description": "What to do"},
				"package": {"type": "string", "description": "Package spec for install (name or name@version); name for remove"}
			},
			"required": ["action"]
		}`),
		Run: func(_ context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Action  string `json:"action"`
				Package string `json:"package"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			client := &pluginmgr.Client{ProfileDir: cfg.ProfileDir}
			switch in.Action {
			case "list":
				names, err := client.List()
				if err != nil {
					return nil, err
				}
				if names == nil {
					names = []string{}
				}
				return map[string]any{"installed": names}, nil
			case "install":
				if strings.TrimSpace(in.Package) == "" {
					return nil, fmt.Errorf("plugin_manager: install needs a package spec")
				}
				spec, err := pluginmgr.ParseSpec(in.Package)
				if err != nil {
					return nil, err
				}
				m, err := client.Install(spec, hostVersion)
				if err != nil {
					return nil, err
				}
				return map[string]any{"installed": m.Name, "version": m.Version}, nil
			case "remove":
				if strings.TrimSpace(in.Package) == "" {
					return nil, fmt.Errorf("plugin_manager: remove needs a package name")
				}
				if err := client.Remove(in.Package); err != nil {
					return nil, err
				}
				return map[string]any{"removed": in.Package}, nil
			default:
				return nil, fmt.Errorf("plugin_manager: unknown action %q", in.Action)
			}
		},
	}
}
