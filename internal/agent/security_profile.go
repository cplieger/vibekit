package agent

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/policyfile"
	"github.com/cplieger/vibekit/internal/settings"
)

// securityPresets resolves the configured security profile into the KAS policy
// preset ids a session opens with, for StartOpts.Presets.
//
// ONE resolver for every session vibekit starts — chat bridges on both session/new
// and session/load, the utility session, and workflow-run bridges — because the
// profile is a global posture and a door that computed its own answer could
// disagree with the others. The utility session matters more than it looks: it is
// the session answering GET /api/permissions, so it is the only place a profile's
// PRESET rules are readable, which is half of what the Customize button
// materialises from. The other half is the loosest rung's own FileRules, which live
// in the user permissions file rather than in any session — see
// policyfile.Profile.FileRules and the seed path in handlePolicyProfile.
//
// An empty result is a real answer and not a failure: it is the Custom profile,
// where the permissions files are the whole policy, and the kascap row withholds
// the wire key entirely for it.
//
// A profile change reaches a chat when its session next starts or loads, because
// KAS exposes no way to change a live session's policy. That is not a silent
// posture change: the setting is global and the user just changed it, and the
// alternative — pinning a chat to the profile it was created under — would need a
// per-chat field the design deliberately does not have.
func securityPresets(ctx context.Context, configDir string) []string {
	var id string
	if !settings.FieldInto(ctx, configDir, settings.KeySecurityProfile, &id) || id == "" {
		p, _ := policyfile.ProfileFor(policyfile.DefaultProfile)
		return p.Presets
	}
	p, ok := policyfile.ProfileFor(id)
	if !ok {
		// WARN and fall back rather than sending nothing. Sending nothing would be
		// the Custom profile, so a typo in config.json would silently remove the
		// fs_read floor and leave the agent asking permission to read a file — a
		// failure that looks like a broken agent rather than a bad setting.
		slog.Warn("unknown security profile in settings; falling back",
			"key", settings.KeySecurityProfile, "value", id, "fallback", policyfile.DefaultProfile)
		p, _ = policyfile.ProfileFor(policyfile.DefaultProfile)
		return p.Presets
	}
	return p.Presets
}
