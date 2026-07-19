package permissions

import (
	"context"

	cfgsettings "github.com/cplieger/vibekit/internal/settings"
)

// SupervisedDefault reports the settings-file-wide default for the
// Supervised-mode flag applied to newly-auto-created chats. Fails CLOSED
// to false on any read/parse problem (see the package comment).
func SupervisedDefault(ctx context.Context, configDir string) bool {
	var b bool
	if !cfgsettings.FieldInto(ctx, configDir, cfgsettings.KeySupervisedDefault, cfgsettings.KeySupervisedDefault, &b) {
		return false
	}
	return b
}
