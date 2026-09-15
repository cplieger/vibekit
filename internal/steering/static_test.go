package steering

import (
	"strings"
	"testing"
)

// fakeEnv substitutes lookupEnv for the test's life. Unset names answer
// (_, false), so a test can pin absence as well as presence.
func fakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := lookupEnv
	lookupEnv = func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
	t.Cleanup(func() { lookupEnv = prev })
}

func TestWriteRuntime_PrintsTheLiveEnv(t *testing.T) {
	fakeEnv(t, map[string]string{
		"HOME":      "/h",
		"KIRO_HOME": "/h/.kiro",
		"PATH":      "/a:/b",
		"GOPATH":    "/g",
		"GOBIN":     "/g/bin",
		"LANG":      "C.UTF-8",
	})
	var b strings.Builder
	writeRuntime(&b, "/cfg")
	out := b.String()

	for _, want := range []string{
		"## Container runtime",
		"`HOME=/h`",
		"`KIRO_HOME=/h/.kiro`",
		"`PATH=/a:/b`",
		"`GOPATH=/g`, `GOBIN=/g/bin`, `LANG=C.UTF-8`",
		"`/cfg` is the persistent volume",
		"`/cfg/tools/bin/gh`",
		"`HOME=/cfg/home`",
		"/etc/profile.d/10-vibekit-path.sh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeRuntime output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TZ=") {
		t.Errorf("writeRuntime printed TZ, which was unset:\n%s", out)
	}
}

// A value carrying a backtick would close the code span it is quoted in, so
// the variable is dropped rather than rendered.
func TestWriteRuntime_BacktickInValueDropsTheVariable(t *testing.T) {
	fakeEnv(t, map[string]string{
		"HOME":   "/h",
		"PATH":   "/a:/b",
		"GOPATH": "/g`\n## Capabilities",
		"LANG":   "C.UTF-8",
	})
	var b strings.Builder
	writeRuntime(&b, "/cfg")
	out := b.String()
	if strings.Contains(out, "GOPATH=") {
		t.Errorf("a GOPATH value with a backtick reached the doc:\n%s", out)
	}
	if !strings.Contains(out, "`LANG=C.UTF-8`") {
		t.Errorf("dropping GOPATH also dropped its clean siblings:\n%s", out)
	}
	if strings.Contains(out, "\n## Capabilities") {
		t.Errorf("a newline in an env value produced a heading:\n%s", out)
	}
}

func TestWriteToolsEngine(t *testing.T) {
	var b strings.Builder
	writeToolsEngine(&b, "/cfg")
	out := b.String()
	for _, want := range []string{
		"localhost:9847/api/tools/search?q=<name>",
		"POST localhost:9847/api/tools",
		`"apt":true`,
		"&unavailable=1",
		"Settings → Tools",
		"`/cfg/tools.json`",
		"`/cfg/tools/opt/<name>/<version>/`",
		`"Refresh catalog"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeToolsEngine output missing %q:\n%s", want, out)
		}
	}
	// The catalog count moves with every refresh; a printed number goes stale.
	if strings.Contains(out, "~870") {
		t.Errorf("writeToolsEngine printed the retired catalog count:\n%s", out)
	}
	// static/index.html's button text is "Refresh catalog"; the longer string is
	// its aria-label, which no user sees.
	if strings.Contains(out, `"Refresh the tool catalog"`) {
		t.Errorf("writeToolsEngine quoted an aria-label as the on-screen label:\n%s", out)
	}
}

func TestWriteGitPanel(t *testing.T) {
	var b strings.Builder
	writeGitPanel(&b, "/w", map[string]bool{kindGitHub: true})
	out := b.String()
	for _, want := range []string{
		"## Git panel",
		"**Changes**",
		"**Pull requests**",
		"**Sources**",
		"git push -u origin <branch>",
		"--ff-only",
		"$HOME/.gitconfig",
		"`/w/<name>`",
		"`gh auth login` plus `gh auth setup-git`",
		"states the scopes caveat",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeGitPanel(github) output missing %q:\n%s", want, out)
		}
	}
}

// The closing paragraph describes the connected forge's login; with none
// connected it would name a caveat the forge section never emitted.
func TestWriteGitPanel_NoForge(t *testing.T) {
	var b strings.Builder
	writeGitPanel(&b, "/w", nil)
	out := b.String()
	if !strings.Contains(out, `Sources → "Add an account"`) {
		t.Errorf("writeGitPanel(none) does not point the user at Sources:\n%s", out)
	}
	for _, absent := range []string{"gh auth login", "scopes caveat"} {
		if strings.Contains(out, absent) {
			t.Errorf("writeGitPanel(none) mentions %q with no forge connected:\n%s", absent, out)
		}
	}

	b.Reset()
	writeGitPanel(&b, "/w", map[string]bool{kindGitLab: true})
	out = b.String()
	if !strings.Contains(out, "are authenticated\n") {
		t.Errorf("writeGitPanel(gitlab) lost the generic login sentence:\n%s", out)
	}
	if strings.Contains(out, "scopes caveat") {
		t.Errorf("writeGitPanel(gitlab) cites the GitHub-only scopes caveat:\n%s", out)
	}
}

// The notifications path is the one a user asks about most; the labels must be
// the exact on-screen text.
func TestWriteUIGuide_NotificationsPath(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b)
	out := b.String()
	for _, want := range []string{
		"Settings → General",
		`"Push notifications"`,
		`"Agent finished"`,
		`"Pull request checks"`,
		`"Workflow runs"`,
		"has no switch of its own",
		// The one off-by-default kind, named with its reason: "why am I not getting
		// pull-request notifications" is otherwise answerable only from source.
		`"Pull request checks" starts OFF`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeUIGuide output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteUIGuide_NamesEveryTab(t *testing.T) {
	var b strings.Builder
	writeUIGuide(&b)
	out := b.String()
	for _, want := range []string{
		`"General", "Tools", "Permissions", "Custom instructions"`,
		`"Toggle git"`,
		`"Global instructions"`,
		"`/docs`",
		"`/history`",
		"`/files`",
		"`/run/<id>`",
		`(tooltip "Cancel this turn")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeUIGuide output missing %q:\n%s", want, out)
		}
	}
	// untrusted_test.go counts exactly one such heading in the whole doc.
	if strings.Contains(out, "\n## Capabilities") {
		t.Errorf("writeUIGuide emitted a second Capabilities heading:\n%s", out)
	}
}

func TestWriteAttachments_UsesTheUploadDirConstant(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/u", "/w")
	out := b.String()
	for _, want := range []string{
		"`/u/pasted-YYYY-MM-DDTHH-MM-SS.png`",
		"`/u/paste-YYYY-MM-DDTHH-MM-SS.txt`",
		"`![label](/w/out/shot.png)`",
		"under `/w/` or `/u/`",
		"`ls -t /u | head`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
	for _, retired := range []string{"/workspace/uploads", "`/uploads/`"} {
		if strings.Contains(out, retired) {
			t.Errorf("writeAttachments hardcoded %q instead of the upload dir parameter:\n%s", retired, out)
		}
	}
}

func TestWriteAttachments_StatesTheCaps(t *testing.T) {
	var b strings.Builder
	writeAttachments(&b, "/u", "/w")
	out := b.String()
	for _, want := range []string{
		"10 MiB",
		"5 MiB base64",
		"15 MiB per prompt",
		"16 images",
		"2000px",
		"49 MiB per gesture, 25 files",
		"the stamp is UTC",
		"NO filename",
		"png/jpg/jpeg/gif/webp/svg/avif/ico/bmp",
		"mp3/wav/ogg/m4a/flac/aac/opus",
		"`.svg` and `.avif` are never inlined",
		"The request was refused as sent",
		"pixel dimensions or image count over its limit",
		"unsupported or mismatched format",
		"does not check whether the model takes images",
		"only a PASTED image is downscaled",
		"multi-line TEXT over 50 lines or 10,000 characters",
		"a `resource` block whose `uri` is the file path",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeAttachments output missing %q:\n%s", want, out)
		}
	}
	// validationGuidance (internal/command/prompt.go) fires only on the
	// backend's payload-validation names, none of which describes a model
	// capability, so a refusal for that cause never carries this sentence.
	for _, retired := range []string{"model without vision", "embedded the same way"} {
		if strings.Contains(out, retired) {
			t.Errorf("writeAttachments carries the retired phrase %q:\n%s", retired, out)
		}
	}
}

// The Limitations bullets are pinned byte-for-byte.
func TestWriteLimitations_Verbatim(t *testing.T) {
	var b strings.Builder
	writeLimitations(&b)
	want := "## Limitations\n\n" +
		"- Shell commands run in the container, not on the host\n" +
		"- No GUI or browser; web_fetch and web_search work\n" +
		"- Container has no Docker socket\n\n"
	if got := b.String(); got != want {
		t.Errorf("writeLimitations() = %q, want %q", got, want)
	}
}

func TestWriteCapabilities(t *testing.T) {
	var b strings.Builder
	writeCapabilities(&b, "/cfg")
	out := b.String()
	for _, want := range []string{
		"`/cfg/chats/*.json`",
		"Undo is per TURN, not per file",
		"no resume or retry tool",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeCapabilities output missing %q:\n%s", want, out)
		}
	}
	// The retired claim: vibekit captures nothing, KAS snapshots only its own
	// edit tools, and the one user affordance is Rewind.
	if strings.Contains(out, "checkpointed server-side") {
		t.Errorf("writeCapabilities repeated the retired per-turn checkpoint claim:\n%s", out)
	}
}
