package permissions

import (
	"context"
	"encoding/json"

	cfgsettings "github.com/cplieger/vibekit/internal/settings"
)

// readSettingsRaw is the shared I/O + parse path for the permission
// readers. It returns the parsed top-level keys from config.json.
// Callers apply their own fail-mode policy to the returned error.
func readSettingsRaw(ctx context.Context, configDir string) (map[string]json.RawMessage, error) {
	data, err := cfgsettings.ReadBytes(ctx, configDir)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// permissionSettings holds all permission-relevant keys extracted from
// a single readSettingsRaw call.
type permissionSettings struct {
	raw         map[string]json.RawMessage
	shellPolicy json.RawMessage
	hasShell    bool
}

// readPermissionSettings extracts all permission-relevant keys from
// config.json in a single unmarshal pass. EvaluateShellCommand calls
// this to read shell_policy without redundant JSON parsing.
func readPermissionSettings(ctx context.Context, configDir string) (*permissionSettings, error) {
	raw, err := readSettingsRaw(ctx, configDir)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return &permissionSettings{}, nil
	}
	ps := &permissionSettings{raw: raw}
	if v, ok := raw[cfgsettings.KeyShellPolicy]; ok {
		ps.shellPolicy = v
		ps.hasShell = true
	}
	return ps, nil
}

// SupervisedDefault reports the settings-file-wide default for the
// Supervised-mode flag applied to newly-auto-created chats.
func SupervisedDefault(ctx context.Context, configDir string) bool {
	var b bool
	if !cfgsettings.FieldInto(ctx, configDir, cfgsettings.KeySupervisedDefault, cfgsettings.KeySupervisedDefault, &b) {
		return false
	}
	return b
}
