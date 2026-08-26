package vibekit

import (
	"strings"
	"testing"
)

// TestNormalizeHookTrigger pins the event-type -> PascalCase trigger map:
// canonical names pass through, v2/IDE camelCase aliases are rewritten
// (case-insensitively), and each resolves to the same trigger its canonical
// spelling does.
func TestNormalizeHookTrigger(t *testing.T) {
	cases := []struct{ in, want string }{
		// Canonical PascalCase passes through.
		{"SessionStart", "SessionStart"},
		{"PostFileSave", "PostFileSave"},
		{"Manual", "Manual"},
		// v2 / Kiro-IDE camelCase aliases map to PascalCase.
		{"fileEdited", "PostFileSave"},
		{"fileCreated", "PostFileCreate"},
		{"fileDeleted", "PostFileDelete"},
		{"userTriggered", "Manual"},
		{"agentStop", "Stop"},
		{"userPromptSubmit", "UserPromptSubmit"},
		// Case-insensitive + trimmed.
		{"POSTFILESAVE", "PostFileSave"},
		{"  fileEdited  ", "PostFileSave"},
		// The three aliases KAS accepts that this map used to be missing.
		{"agentSpawn", "SessionStart"},
		{"SessionEnd", "Stop"},
		{"AfterFileEdit", "PostFileSave"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := NormalizeHookTrigger(tc.in)
			if !ok {
				t.Fatalf("NormalizeHookTrigger(%q) reported unknown, want %q", tc.in, tc.want)
			}
			if got.Name != tc.want {
				t.Errorf("NormalizeHookTrigger(%q).Name = %q, want %q", tc.in, got.Name, tc.want)
			}
		})
	}
}

// TestNormalizeHookTrigger_RejectsUnknown is the inverted case, and the inversion
// is the point. This used to pass an unknown trigger through trimmed, which read
// as leniency and behaved as silence: KAS's parseHookDocument DROPS a hook whose
// trigger it does not recognise, so create_hook answered 200 with a file path for
// a hook that loads nowhere, never fires and never appears in /api/hooks.
func TestNormalizeHookTrigger_RejectsUnknown(t *testing.T) {
	for _, in := range []string{"someFutureTrigger", "  x  ", "", "PostFileSaved!"} {
		if got, ok := NormalizeHookTrigger(in); ok {
			t.Errorf("NormalizeHookTrigger(%q) = (%q, true), want it reported unknown so the "+
				"caller can refuse instead of writing a hook KAS will discard", in, got.Name)
		}
	}
}

// TestKnownHookTriggers_NamesTheAcceptedSet guards the error message rather than
// the refusal, because a rejection that does not say what IS accepted just moves
// the guessing from the server to the user.
func TestKnownHookTriggers_NamesTheAcceptedSet(t *testing.T) {
	got := KnownHookTriggers()
	for _, want := range []string{"SessionStart", "Stop", "PreToolUse", "PostToolUse", "Manual"} {
		if !strings.Contains(got, want) {
			t.Errorf("KnownHookTriggers() = %q, missing %q", got, want)
		}
	}
	// Deduped: every trigger has several aliases mapping onto it.
	if strings.Count(got, "PostFileSave") != 1 {
		t.Errorf("KnownHookTriggers() = %q, want PostFileSave listed once", got)
	}
}

// TestEveryCanonicalTriggerHasASubject is what keeps ClassifyHookMatcher TOTAL.
//
// The classifier switches on the subject and reports nothing for a value it does
// not recognise, so a trigger added to the map with a zero-value subject would
// silently opt out of both diagnostics — the exact shape of failure the
// diagnostics exist to remove. The set is closed and small, so listing the
// expected partition here rather than deriving it is deliberate: a derived
// expectation would agree with any map, including a wrong one.
func TestEveryCanonicalTriggerHasASubject(t *testing.T) {
	want := map[string]HookMatcherSubject{
		"PreToolUse":       HookMatcherSubjectToolName,
		"PostToolUse":      HookMatcherSubjectToolName,
		"PostFileCreate":   HookMatcherSubjectFilePath,
		"PostFileSave":     HookMatcherSubjectFilePath,
		"PostFileDelete":   HookMatcherSubjectFilePath,
		"SessionStart":     HookMatcherSubjectNone,
		"Stop":             HookMatcherSubjectNone,
		"UserPromptSubmit": HookMatcherSubjectNone,
		"PreTaskExec":      HookMatcherSubjectNone,
		"PostTaskExec":     HookMatcherSubjectNone,
		"Manual":           HookMatcherSubjectNone,
	}

	// Every alias agrees with its canonical name, which is what makes an alias a
	// spelling rather than a second trigger.
	seen := make(map[string]bool, len(want))
	for alias, meta := range hookTriggers {
		w, ok := want[meta.Name]
		if !ok {
			t.Errorf("alias %q maps to trigger %q, which this test does not account for; "+
				"add it with its subject or ClassifyHookMatcher will silently ignore it", alias, meta.Name)
			continue
		}
		if meta.Subject != w {
			t.Errorf("alias %q gives %s the subject %q, want %q; an alias disagreeing with its "+
				"canonical name means one spelling gets a diagnostic and another does not",
				alias, meta.Name, meta.Subject, w)
		}
		seen[meta.Name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("trigger %s is expected in the table and absent from it", name)
		}
	}
}

// TestClassifyHookMatcher covers the pairing rule in both directions plus the
// three cases that must report NOTHING, because a false positive here is a 400 on
// a hook the user was right to create.
func TestClassifyHookMatcher(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
		matcher string
		want    HookMatcherDefect
	}{
		// none-subject + a matcher: ignored upstream, so refused at creation.
		{"session start with a matcher", "SessionStart", `\.go$`, HookMatcherIneffective},
		{"stop with a matcher", "Stop", "anything", HookMatcherIneffective},
		{"manual with a matcher", "Manual", "x", HookMatcherIneffective},
		// The alias spelling has to reach the same verdict, or the check is
		// bypassable by writing the trigger differently.
		{"an alias reaches the same verdict", "userTriggered", "x", HookMatcherIneffective},
		// Whitespace is not a matcher. buildHookDoc TrimSpaces the value into the
		// file, so treating "   " as present would refuse a hook whose stored
		// matcher is empty anyway.
		{"whitespace is not a matcher", "SessionStart", "   ", HookMatcherOK},
		{"session start with no matcher", "SessionStart", "", HookMatcherOK},
		// toolName-subject with no matcher: legitimate, so a badge and not a
		// refusal.
		{"pre tool use with no matcher", "PreToolUse", "", HookMatcherMissingToolName},
		{"post tool use with no matcher", "PostToolUse", "  ", HookMatcherMissingToolName},
		{"pre tool use with a matcher", "PreToolUse", "fsWrite", HookMatcherOK},
		// filePath-subject: effective either way, so neither direction reports.
		{"file save with a matcher", "PostFileSave", `\.go$`, HookMatcherOK},
		{"file save with no matcher", "PostFileSave", "", HookMatcherOK},
		// An unknown trigger is a different and larger defect, already refused by
		// NormalizeHookTrigger's second return. Reporting a matcher complaint
		// about it would name the wrong problem.
		{"unknown trigger reports nothing", "someFutureTrigger", "x", HookMatcherOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyHookMatcher(tc.trigger, tc.matcher); got != tc.want {
				t.Errorf("ClassifyHookMatcher(%q, %q) = %q, want %q", tc.trigger, tc.matcher, got, tc.want)
			}
		})
	}
}
