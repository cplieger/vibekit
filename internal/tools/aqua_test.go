package tools

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadFixture parses a real aqua-registry package file from testdata.
// The fixtures are verbatim copies of upstream registry.yaml files, so
// these tests double as a guard on the YAML struct tags the catalog
// compiler relies on.
func loadFixture(t *testing.T, name string) *AquaPackage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc struct {
		Packages []AquaPackage `yaml:"packages"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	if len(doc.Packages) != 1 {
		t.Fatalf("fixture %s: want 1 package, got %d", name, len(doc.Packages))
	}
	return &doc.Packages[0]
}

func resolveOrFatal(t *testing.T, p *AquaPackage, version, arch string) *InstallSpec {
	t.Helper()
	spec, err := p.resolveSpecFor(version, arch)
	if err != nil {
		t.Fatalf("resolve %s %s/%s: %v", version, "linux", arch, err)
	}
	return spec
}

func TestResolveSpec_GH_CatchAllOverride(t *testing.T) {
	p := loadFixture(t, "gh.yaml")
	spec := resolveOrFatal(t, p, "v2.96.0", "amd64")

	wantURL := "https://github.com/cli/cli/releases/download/v2.96.0/gh_2.96.0_linux_amd64.tar.gz"
	if spec.URL != wantURL {
		t.Errorf("URL = %s, want %s", spec.URL, wantURL)
	}
	// The linux goos override flips the root zip format to tar.gz.
	if spec.Format != "tar.gz" {
		t.Errorf("Format = %s, want tar.gz", spec.Format)
	}
	if len(spec.Files) != 1 || spec.Files[0].Name != "gh" {
		t.Fatalf("Files = %+v, want one gh entry", spec.Files)
	}
	if spec.Files[0].Src != "gh_2.96.0_linux_amd64/bin/gh" {
		t.Errorf("Files[0].Src = %s", spec.Files[0].Src)
	}
	wantSum := "https://github.com/cli/cli/releases/download/v2.96.0/gh_2.96.0_checksums.txt"
	if spec.ChecksumURL != wantSum {
		t.Errorf("ChecksumURL = %s, want %s", spec.ChecksumURL, wantSum)
	}
	if spec.ChecksumAlg != "sha256" {
		t.Errorf("ChecksumAlg = %s, want sha256", spec.ChecksumAlg)
	}
}

func TestResolveSpec_GH_OldVersionRange(t *testing.T) {
	p := loadFixture(t, "gh.yaml")
	// 2.25.0 falls in the semver("<= 2.27.0") override: tar.gz format,
	// macOS replacement only affects darwin — linux stays linux.
	spec := resolveOrFatal(t, p, "v2.25.0", "arm64")
	wantURL := "https://github.com/cli/cli/releases/download/v2.25.0/gh_2.25.0_linux_arm64.tar.gz"
	if spec.URL != wantURL {
		t.Errorf("URL = %s, want %s", spec.URL, wantURL)
	}
}

func TestResolveSpec_Shellcheck_ArchReplacements(t *testing.T) {
	p := loadFixture(t, "shellcheck.yaml")
	spec := resolveOrFatal(t, p, "v0.11.0", "arm64")
	wantURL := "https://github.com/koalaman/shellcheck/releases/download/v0.11.0/shellcheck-v0.11.0.linux.aarch64.tar.xz"
	if spec.URL != wantURL {
		t.Errorf("URL = %s, want %s", spec.URL, wantURL)
	}
	if spec.Format != "tar.xz" {
		t.Errorf("Format = %s, want tar.xz", spec.Format)
	}
	if spec.Files[0].Src != "shellcheck-v0.11.0/shellcheck" {
		t.Errorf("Files[0].Src = %s", spec.Files[0].Src)
	}

	amd := resolveOrFatal(t, p, "v0.11.0", "amd64")
	wantAmd := "https://github.com/koalaman/shellcheck/releases/download/v0.11.0/shellcheck-v0.11.0.linux.x86_64.tar.xz"
	if amd.URL != wantAmd {
		t.Errorf("amd64 URL = %s, want %s", amd.URL, wantAmd)
	}
}

func TestResolveSpec_Shellcheck_NoAssetRange(t *testing.T) {
	p := loadFixture(t, "shellcheck.yaml")
	if _, err := p.resolveSpecFor("v0.4.0", "amd64"); err == nil {
		t.Fatal("want error for no_asset version range")
	}
}

func TestResolveSpec_Shellcheck_OldRange_SupportedEnvs(t *testing.T) {
	p := loadFixture(t, "shellcheck.yaml")
	// v0.5.0 sits in the <= 0.6.0 override whose supported_envs are
	// linux/amd64 + windows: amd64 resolves, arm64 refuses.
	spec := resolveOrFatal(t, p, "v0.5.0", "amd64")
	wantURL := "https://github.com/koalaman/shellcheck/releases/download/v0.5.0/shellcheck-v0.5.0.linux.x86_64.tar.xz"
	if spec.URL != wantURL {
		t.Errorf("URL = %s, want %s", spec.URL, wantURL)
	}
	if _, err := p.resolveSpecFor("v0.5.0", "arm64"); err == nil {
		t.Fatal("want unsupported-env error for arm64 at v0.5.0")
	}
}

func TestResolveSpec_Node_HTTPType(t *testing.T) {
	p := loadFixture(t, "node.yaml")
	spec := resolveOrFatal(t, p, "v24.13.0", "amd64")
	wantURL := "https://nodejs.org/dist/v24.13.0/node-v24.13.0-linux-x64.tar.gz"
	if spec.URL != wantURL {
		t.Errorf("URL = %s, want %s", spec.URL, wantURL)
	}
	// arm64 has no replacement entry: stays arm64.
	arm := resolveOrFatal(t, p, "v24.13.0", "arm64")
	wantArm := "https://nodejs.org/dist/v24.13.0/node-v24.13.0-linux-arm64.tar.gz"
	if arm.URL != wantArm {
		t.Errorf("arm URL = %s, want %s", arm.URL, wantArm)
	}
	// The current (catch-all) override ships node/npm/npx files.
	if len(spec.Files) != 3 {
		t.Fatalf("Files = %+v, want 3", spec.Files)
	}
	if spec.Files[1].Name != "npm" || spec.Files[1].Src != "node-v24.13.0-linux-x64/bin/npm" {
		t.Errorf("npm file = %+v", spec.Files[1])
	}
}

func TestResolveSpec_Go_TrimPrefixTemplate(t *testing.T) {
	p := loadFixture(t, "go.yaml")
	spec := resolveOrFatal(t, p, "go1.23.4", "amd64")
	wantURL := "https://golang.org/dl/go1.23.4.linux-amd64.tar.gz"
	if spec.URL != wantURL {
		t.Errorf("URL = %s, want %s", spec.URL, wantURL)
	}
	if spec.Files[0].Name != "go" || spec.Files[0].Src != "go/bin/go" {
		t.Errorf("Files[0] = %+v", spec.Files[0])
	}
}

func TestResolveSpec_GolangciLint_Checksum(t *testing.T) {
	p := loadFixture(t, "golangci-lint.yaml")
	spec := resolveOrFatal(t, p, "v2.12.2", "arm64")
	wantURL := "https://github.com/golangci/golangci-lint/releases/download/v2.12.2/golangci-lint-2.12.2-linux-arm64.tar.gz"
	if spec.URL != wantURL {
		t.Errorf("URL = %s, want %s", spec.URL, wantURL)
	}
	if spec.ChecksumURL == "" {
		t.Error("want a checksum URL for golangci-lint")
	}
}

func TestEvalVersionFilter_GoTags(t *testing.T) {
	p := loadFixture(t, "go.yaml")
	cases := []struct {
		tag  string
		want bool
	}{
		{"go1.23.4", true},
		{"go1.24rc1", false},
		{"go1.22beta2", false},
		{"weekly.2011-11-01", false},
	}
	for _, c := range cases {
		if got := evalVersionFilter(p.VersionFilter, c.tag, p.VersionPrefix); got != c.want {
			t.Errorf("filter(%q) = %v, want %v", c.tag, got, c.want)
		}
	}
}

func TestEvalConstraint(t *testing.T) {
	cases := []struct {
		constraint string
		version    string
		want       bool
	}{
		{"true", "v1.0.0", true},
		{"", "v1.0.0", true},
		{"false", "v1.0.0", false},
		{`semver("<= 2.27.0")`, "v2.25.0", true},
		{`semver("<= 2.27.0")`, "v2.28.0", false},
		{`semver(">= 1.0.0")`, "v0.9.0", false},
		{`semver("< 2.0.0")`, "not-a-version", false},
		{"garbage(((", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := evalConstraint(c.constraint, c.version, ""); got != c.want {
			t.Errorf("evalConstraint(%q, %q) = %v, want %v", c.constraint, c.version, got, c.want)
		}
	}
}

func TestFindChecksum(t *testing.T) {
	list := "abc123def456abc123def456abc123def456abc123def456abc123def456abcd  gh_2.96.0_linux_amd64.tar.gz\n" +
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff  gh_2.96.0_macOS_amd64.zip\n"
	if got := findChecksum(list, "gh_2.96.0_linux_amd64.tar.gz"); got != "abc123def456abc123def456abc123def456abc123def456abc123def456abcd" {
		t.Errorf("list lookup = %q", got)
	}
	if got := findChecksum(list, "missing.tar.gz"); got != "" {
		t.Errorf("missing asset = %q, want empty", got)
	}
	bare := "abc123def456abc123def456abc123def456abc123def456abc123def456abcd\n"
	if got := findChecksum(bare, "anything.tar.gz"); got == "" {
		t.Error("bare digest file should match any asset")
	}
}

func TestEnvSupported(t *testing.T) {
	cases := []struct {
		envs []string
		arch string
		want bool
	}{
		{nil, "amd64", true},
		{[]string{"darwin", "windows"}, "amd64", false},
		{[]string{"darwin", "windows", "amd64"}, "amd64", true},
		{[]string{"linux/amd64"}, "arm64", false},
		{[]string{"linux"}, "arm64", true},
		{[]string{"all"}, "arm64", true},
	}
	for _, c := range cases {
		if got := envSupported(c.envs, c.arch); got != c.want {
			t.Errorf("envSupported(%v, %s) = %v, want %v", c.envs, c.arch, got, c.want)
		}
	}
}

func TestValidVersionString(t *testing.T) {
	good := []string{"v2.96.0", "1.1.411", "go1.23.4", "jdk-21.0.5+11", "2025-01-06"}
	for _, v := range good {
		if !validVersionString(v) {
			t.Errorf("validVersionString(%q) = false, want true", v)
		}
	}
	bad := []string{"", "v1.0.0; rm -rf /", "a b", "$(evil)", "v1/../../etc"}
	for _, v := range bad {
		if validVersionString(v) {
			t.Errorf("validVersionString(%q) = true, want false", v)
		}
	}
}
