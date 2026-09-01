package agent

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/policyfile"
	"github.com/cplieger/vibekit/internal/settings"
)

// securityPresets resolves the configured security profile into the KAS policy
// preset ids a session opens with, for StartOpts.Presets. One resolver for every
// session vibekit starts, so no spawn site can disagree about the global posture.
//
// An empty result is the Custom profile (permissions files are the whole
// policy), not a failure; the kascap row withholds the wire key entirely for it.
// A profile change reaches a chat only at its next session start/load — KAS has
// no live-session policy update.
func securityPresets(ctx context.Context, configDir string) []string {
	var id string
	if !settings.FieldInto(ctx, configDir, settings.KeySecurityProfile, &id) || id == "" {
		p, _ := policyfile.ProfileFor(policyfile.DefaultProfile)
		return p.Presets
	}
	p, ok := policyfile.ProfileFor(id)
	if !ok {
		// Fall back rather than send nothing: nothing means Custom, which would
		// silently drop the fs_read floor on a config.json typo.
		slog.Warn("unknown security profile in settings; falling back",
			"key", settings.KeySecurityProfile, "value", id, "fallback", policyfile.DefaultProfile)
		p, _ = policyfile.ProfileFor(policyfile.DefaultProfile)
		return p.Presets
	}
	return p.Presets
}
