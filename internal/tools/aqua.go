package tools

import (
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"text/template"

	"github.com/expr-lang/expr"
	goversion "github.com/hashicorp/go-version"
)

// AquaPackage is the subset of an aqua-registry package definition that
// vibekit evaluates. Field names mirror the upstream YAML/JSON schema
// (https://aquaproj.github.io/docs/reference/registry-config) so the
// catalog compiler can unmarshal registry files directly. Windows- and
// darwin-only concerns (rosetta2, windows_arm_emulation,
// complete_windows_ext) are intentionally absent: vibekit runs linux
// only, and the evaluator resolves everything for linux/GOARCH.
type AquaPackage struct {
	Type             string                `json:"type" yaml:"type"`
	RepoOwner        string                `json:"repo_owner,omitempty" yaml:"repo_owner"`
	RepoName         string                `json:"repo_name,omitempty" yaml:"repo_name"`
	Description      string                `json:"description,omitempty" yaml:"description"`
	Asset            string                `json:"asset,omitempty" yaml:"asset"`
	URL              string                `json:"url,omitempty" yaml:"url"`
	Path             string                `json:"path,omitempty" yaml:"path"`
	Format           string                `json:"format,omitempty" yaml:"format"`
	Files            []AquaFile            `json:"files,omitempty" yaml:"files"`
	Replacements     map[string]string     `json:"replacements,omitempty" yaml:"replacements"`
	Overrides        []AquaOverride        `json:"overrides,omitempty" yaml:"overrides"`
	SupportedEnvs    []string              `json:"supported_envs,omitempty" yaml:"supported_envs"`
	VersionConstr    string                `json:"version_constraint,omitempty" yaml:"version_constraint"`
	VersionOverrides []AquaVersionOverride `json:"version_overrides,omitempty" yaml:"version_overrides"`
	VersionFilter    string                `json:"version_filter,omitempty" yaml:"version_filter"`
	VersionSource    string                `json:"version_source,omitempty" yaml:"version_source"`
	VersionPrefix    string                `json:"version_prefix,omitempty" yaml:"version_prefix"`
	Checksum         *AquaChecksum         `json:"checksum,omitempty" yaml:"checksum"`
	NoAsset          bool                  `json:"no_asset,omitempty" yaml:"no_asset"`
}

// AquaFile names one binary inside the extracted artifact.
type AquaFile struct {
	Name string `json:"name" yaml:"name"`
	Src  string `json:"src,omitempty" yaml:"src"` // template; default = Name
}

// AquaOverride adjusts fields for a specific GOOS/GOARCH.
type AquaOverride struct {
	GOOS         string            `json:"goos,omitempty" yaml:"goos"`
	GOArch       string            `json:"goarch,omitempty" yaml:"goarch"`
	Asset        string            `json:"asset,omitempty" yaml:"asset"`
	URL          string            `json:"url,omitempty" yaml:"url"`
	Format       string            `json:"format,omitempty" yaml:"format"`
	Files        []AquaFile        `json:"files,omitempty" yaml:"files"`
	Replacements map[string]string `json:"replacements,omitempty" yaml:"replacements"`
	Checksum     *AquaChecksum     `json:"checksum,omitempty" yaml:"checksum"`
}

// AquaVersionOverride switches definition fields per version range. A
// nil pointer field means "inherit from the package root".
type AquaVersionOverride struct {
	VersionConstr string             `json:"version_constraint,omitempty" yaml:"version_constraint"`
	Asset         *string            `json:"asset,omitempty" yaml:"asset"`
	URL           *string            `json:"url,omitempty" yaml:"url"`
	Format        *string            `json:"format,omitempty" yaml:"format"`
	Files         []AquaFile         `json:"files,omitempty" yaml:"files"`
	Replacements  *map[string]string `json:"replacements,omitempty" yaml:"replacements"`
	Overrides     []AquaOverride     `json:"overrides,omitempty" yaml:"overrides"`
	SupportedEnvs []string           `json:"supported_envs,omitempty" yaml:"supported_envs"`
	Checksum      *AquaChecksum      `json:"checksum,omitempty" yaml:"checksum"`
	NoAsset       *bool              `json:"no_asset,omitempty" yaml:"no_asset"`
	VersionPrefix *string            `json:"version_prefix,omitempty" yaml:"version_prefix"`
}

// AquaChecksum describes where the artifact's checksum lives.
type AquaChecksum struct {
	Type      string `json:"type,omitempty" yaml:"type"` // github_release | http
	Asset     string `json:"asset,omitempty" yaml:"asset"`
	URL       string `json:"url,omitempty" yaml:"url"`
	Algorithm string `json:"algorithm,omitempty" yaml:"algorithm"` // sha256 | sha512 | ...
	Enabled   *bool  `json:"enabled,omitempty" yaml:"enabled"`
}

// InstallSpec is the fully resolved plan for downloading one tool
// version on this machine: a URL, an archive format, the binaries to
// link, and an optional checksum source.
type InstallSpec struct {
	URL         string
	Format      string // tar.gz | tar.xz | tar.zst | zip | gz | xz | raw
	Files       []AquaFile
	ChecksumURL string // empty = no verification
	ChecksumAlg string
}

// templateVars is the variable set aqua templates reference.
type templateVars struct {
	Version string
	SemVer  string
	OS      string
	Arch    string
	Format  string
	// Asset is the artifact filename; AssetWithoutExt strips the
	// format extension (used by files[].src on archives whose top
	// directory matches the asset name, e.g. astral-sh/uv).
	Asset           string
	AssetWithoutExt string
}

// aquaTemplateFuncs mirrors the template helpers aqua provides (the
// subset observed across the standard registry).
var aquaTemplateFuncs = template.FuncMap{
	"trimV": func(s string) string { return strings.TrimPrefix(s, "v") },
	// sprig argument order: trimPrefix PREFIX STR / trimSuffix SUFFIX STR.
	"trimPrefix": func(prefix, s string) string { return strings.TrimPrefix(s, prefix) },
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
	"title":      titleCase,
	"toLower":    strings.ToLower,
	"toUpper":    strings.ToUpper,
	// sprig argument order: replace OLD NEW SRC.
	"replace": func(old, newStr, src string) string { return strings.ReplaceAll(src, old, newStr) },
}

// titleCase upper-cases the first rune only (sprig's title semantics
// are per-word, but registry usage is single-word OS names).
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ResolveSpec evaluates the package definition for the given version on
// linux/GOARCH and returns the download plan. version is the upstream
// tag (with any version_prefix still attached).
func (p *AquaPackage) ResolveSpec(version string) (*InstallSpec, error) {
	return p.resolveSpecFor(version, runtime.GOARCH)
}

// resolveSpecFor is ResolveSpec with the arch injectable for tests.
func (p *AquaPackage) resolveSpecFor(version, goarch string) (*InstallSpec, error) {
	d, err := p.flatten(version)
	if err != nil {
		return nil, err
	}
	if d.noAsset {
		return nil, fmt.Errorf("aqua: %s/%s %s ships no asset", p.RepoOwner, p.RepoName, version)
	}
	if !envSupported(d.supportedEnvs, goarch) {
		return nil, fmt.Errorf("aqua: %s/%s %s not available for linux/%s", p.RepoOwner, p.RepoName, version, goarch)
	}
	d.applyOverride(goarch)

	format := d.format
	if format == "" {
		format = "raw"
	}
	vars := templateVars{
		Version: version,
		SemVer:  strings.TrimPrefix(strings.TrimPrefix(version, d.versionPrefix), "v"),
		OS:      replaced(d.replacements, "linux"),
		Arch:    replaced(d.replacements, goarch),
		Format:  formatExt(format),
	}

	url, err := d.artifactURL(p, vars)
	if err != nil {
		return nil, err
	}
	spec := &InstallSpec{URL: url, Format: format}
	vars.Asset = lastPathSegment(url)
	vars.AssetWithoutExt = strings.TrimSuffix(vars.Asset, "."+formatExt(format))

	files := d.files
	if len(files) == 0 {
		files = []AquaFile{{Name: p.RepoName}}
	}
	for _, f := range files {
		src := f.Src
		if src != "" {
			s, err := renderTemplate(src, vars)
			if err != nil {
				return nil, fmt.Errorf("file src template: %w", err)
			}
			src = s
		}
		spec.Files = append(spec.Files, AquaFile{Name: f.Name, Src: src})
	}

	if c := d.checksum; c != nil && (c.Enabled == nil || *c.Enabled) {
		// A CONFIGURED checksum must resolve or the install fails —
		// silently downgrading to an unverified install on a template
		// error or unknown type would hide exactly the tampering the
		// checksum exists to catch.
		if c.Algorithm == "" {
			return nil, fmt.Errorf("aqua: checksum configured without an algorithm")
		}
		cu, err := d.checksumURL(p, c, vars)
		if err != nil {
			return nil, fmt.Errorf("aqua: checksum url: %w", err)
		}
		if cu == "" {
			return nil, fmt.Errorf("aqua: unsupported checksum type %q", c.Type)
		}
		spec.ChecksumURL = cu
		spec.ChecksumAlg = c.Algorithm
	}
	return spec, nil
}

// flatDef is the package definition after version-override resolution:
// one concrete set of fields for the version being installed.
type flatDef struct {
	asset         string
	url           string
	format        string
	files         []AquaFile
	replacements  map[string]string
	overrides     []AquaOverride
	supportedEnvs []string
	checksum      *AquaChecksum
	noAsset       bool
	versionPrefix string
}

// flatten resolves version_overrides: the first override whose
// version_constraint matches wins; no match (or none defined) keeps the
// root fields when the root constraint allows the version.
func (p *AquaPackage) flatten(version string) (*flatDef, error) {
	d := &flatDef{
		asset:         p.Asset,
		url:           p.URL,
		format:        p.Format,
		files:         p.Files,
		replacements:  p.Replacements,
		overrides:     p.Overrides,
		supportedEnvs: p.SupportedEnvs,
		checksum:      p.Checksum,
		noAsset:       p.NoAsset,
		versionPrefix: p.VersionPrefix,
	}
	rootOK := p.VersionConstr == "" || evalConstraint(p.VersionConstr, version, d.versionPrefix)
	if rootOK {
		return d, nil
	}
	for i := range p.VersionOverrides {
		vo := &p.VersionOverrides[i]
		if !evalConstraint(vo.VersionConstr, version, d.versionPrefix) {
			continue
		}
		if vo.Asset != nil {
			d.asset = *vo.Asset
		}
		if vo.URL != nil {
			d.url = *vo.URL
		}
		if vo.Format != nil {
			d.format = *vo.Format
		}
		if vo.Files != nil {
			d.files = vo.Files
		}
		if vo.Replacements != nil {
			d.replacements = *vo.Replacements
		}
		if vo.Overrides != nil {
			d.overrides = vo.Overrides
		}
		if vo.SupportedEnvs != nil {
			d.supportedEnvs = vo.SupportedEnvs
		}
		if vo.Checksum != nil {
			d.checksum = vo.Checksum
		}
		if vo.NoAsset != nil {
			d.noAsset = *vo.NoAsset
		}
		if vo.VersionPrefix != nil {
			d.versionPrefix = *vo.VersionPrefix
		}
		return d, nil
	}
	return nil, fmt.Errorf("aqua: no version_override matches %q", version)
}

// applyOverride merges the first matching goos/goarch override for
// linux/goarch into the flat definition.
func (d *flatDef) applyOverride(goarch string) {
	for i := range d.overrides {
		o := &d.overrides[i]
		if o.GOOS != "" && o.GOOS != "linux" {
			continue
		}
		if o.GOArch != "" && o.GOArch != goarch {
			continue
		}
		if o.Asset != "" {
			d.asset = o.Asset
		}
		if o.URL != "" {
			d.url = o.URL
		}
		if o.Format != "" {
			d.format = o.Format
		}
		if o.Files != nil {
			d.files = o.Files
		}
		if o.Replacements != nil {
			// Override replacements MERGE onto the base map (aqua
			// semantics: linux-specific arm64->aarch64 on top of the
			// root's amd64->x86_64).
			merged := map[string]string{}
			for k, v := range d.replacements {
				merged[k] = v
			}
			for k, v := range o.Replacements {
				merged[k] = v
			}
			d.replacements = merged
		}
		if o.Checksum != nil {
			d.checksum = o.Checksum
		}
		return
	}
}

// artifactURL renders the download URL for the definition: http types
// carry a full url template; github_release composes the release
// download URL from the asset template; github_content downloads a raw
// file from the repo at the version tag.
func (d *flatDef) artifactURL(p *AquaPackage, vars templateVars) (string, error) {
	switch p.Type {
	case "http":
		if d.url == "" {
			return "", fmt.Errorf("aqua: http package without url")
		}
		return renderTemplate(d.url, vars)
	case "github_release":
		if d.asset == "" {
			return "", fmt.Errorf("aqua: github_release package without asset")
		}
		asset, err := renderTemplate(d.asset, vars)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
			p.RepoOwner, p.RepoName, vars.Version, asset), nil
	case "github_content":
		if p.Path == "" {
			return "", fmt.Errorf("aqua: github_content package without path")
		}
		path, err := renderTemplate(p.Path, vars)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s",
			p.RepoOwner, p.RepoName, vars.Version, path), nil
	default:
		return "", fmt.Errorf("aqua: unsupported package type %q", p.Type)
	}
}

// checksumURL renders the checksum artifact URL.
func (d *flatDef) checksumURL(p *AquaPackage, c *AquaChecksum, vars templateVars) (string, error) {
	switch c.Type {
	case "github_release":
		asset, err := renderTemplate(c.Asset, vars)
		if err != nil || asset == "" {
			return "", err
		}
		return fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
			p.RepoOwner, p.RepoName, vars.Version, asset), nil
	case "http":
		return renderTemplate(c.URL, vars)
	default:
		return "", nil // unsupported checksum source: skip verification
	}
}

// renderTemplate executes an aqua Go template with the standard vars
// and helper funcs.
func renderTemplate(tmpl string, vars templateVars) (string, error) {
	t, err := template.New("aqua").Funcs(aquaTemplateFuncs).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", tmpl, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, vars); err != nil {
		return "", fmt.Errorf("execute template %q: %w", tmpl, err)
	}
	return b.String(), nil
}

// evalConstraint evaluates an aqua version_constraint expression
// ("true", "false", `semver(">= 1.2")`, boolean combinations) against
// the version. Unparseable expressions or versions yield false — the
// caller then falls through to the next override, and installation
// fails with a clear "no override matches" error rather than a wrong
// asset URL.
func evalConstraint(constraint, version, prefix string) bool {
	switch strings.TrimSpace(constraint) {
	case "", "true":
		return true
	case "false":
		return false
	}
	out, err := evalExpr(constraint, version, prefix)
	if err != nil {
		return false
	}
	b, ok := out.(bool)
	return ok && b
}

// evalVersionFilter evaluates an aqua version_filter expression against
// a candidate version tag. Errors count as "filtered out".
func evalVersionFilter(filter, version, prefix string) bool {
	if strings.TrimSpace(filter) == "" {
		return true
	}
	out, err := evalExpr(filter, version, prefix)
	if err != nil {
		return false
	}
	b, ok := out.(bool)
	return ok && b
}

// evalExpr runs an expr-lang expression with aqua's environment: the
// Version string, a semver(constraint) matcher bound to that version,
// and a SemVer convenience string. The semver matcher uses
// hashicorp/go-version — the SAME library aqua binds (its
// pkg/expr/version_compare.go) — because prerelease semantics differ
// across semver libraries (Masterminds excludes prereleases from plain
// ranges; go-version compares them), and registry version_overrides
// were authored against aqua's behavior.
func evalExpr(src, version, prefix string) (any, error) {
	semverStr := strings.TrimPrefix(strings.TrimPrefix(version, prefix), "v")
	env := map[string]any{
		"Version":           version,
		"SemVer":            semverStr,
		"semver":            func(constraint string) bool { return checkConstraint(constraint, semverStr) },
		"semverWithVersion": func(constraint, ver string) bool { return checkConstraint(constraint, strings.TrimPrefix(ver, "v")) },
		"trimPrefix":        strings.TrimPrefix,
	}
	prog, err := expr.Compile(src, expr.Env(env), expr.AllowUndefinedVariables())
	if err != nil {
		return nil, err
	}
	return expr.Run(prog, env)
}

// commitHashRe matches full git commit hashes, which aqua's comparator
// rejects outright (not comparable as versions).
var commitHashRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// constraintOps is aqua's operator table, in aqua's match order
// (two-char operators first so ">=" isn't misparsed as ">").
var constraintOps = []struct {
	op  string
	cmp func(a, b *goversion.Version) bool
}{
	{">=", func(a, b *goversion.Version) bool { return a.GreaterThanOrEqual(b) }},
	{"<=", func(a, b *goversion.Version) bool { return a.LessThanOrEqual(b) }},
	{"!=", func(a, b *goversion.Version) bool { return !a.Equal(b) }},
	{">", func(a, b *goversion.Version) bool { return a.GreaterThan(b) }},
	{"<", func(a, b *goversion.Version) bool { return a.LessThan(b) }},
	{"=", func(a, b *goversion.Version) bool { return a.Equal(b) }},
}

// checkConstraint evaluates an aqua version constraint ("<= 2.27.0",
// ">= 1.0, < 2.0") against a version. This is a direct port of aqua's
// pkg/expr/version_compare.go compare(): comma = AND, six operators,
// direct go-version Compare semantics (prereleases order by semver
// precedence — deliberately NOT go-version's Constraints.Check, whose
// prerelease gating aqua does not use), commit hashes never match, and
// an unparseable constraint or version is false.
func checkConstraint(constraint, ver string) bool {
	if commitHashRe.MatchString(ver) {
		return false
	}
	v, err := goversion.NewVersion(ver)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(strings.TrimSpace(constraint), ",") {
		c := strings.TrimSpace(part)
		matched := false
		for _, comp := range constraintOps {
			rest := strings.TrimPrefix(c, comp.op)
			if rest == c {
				continue
			}
			bound, err := goversion.NewVersion(strings.TrimSpace(rest))
			if err != nil {
				return false
			}
			if !comp.cmp(v, bound) {
				return false
			}
			matched = true
			break
		}
		if !matched {
			return false // aqua panics on an unknown operator; we fail soft
		}
	}
	return true
}

// envSupported reports whether linux/goarch is inside supported_envs.
// An empty list means all platforms. Entries may be "all", a GOOS, a
// GOOS/GOARCH pair, or a bare GOARCH (which matches any OS of that
// arch, per aqua's semantics).
func envSupported(envs []string, goarch string) bool {
	if len(envs) == 0 {
		return true
	}
	return slices.ContainsFunc(envs, func(e string) bool {
		return e == "all" || e == "linux" || e == goarch || e == "linux/"+goarch
	})
}

// replaced maps a template var through the replacements table.
func replaced(repl map[string]string, key string) string {
	if v, ok := repl[key]; ok {
		return v
	}
	return key
}

// formatExt converts an aqua format to the extension templates splice
// into asset names ({{.Format}}). "raw" has no extension.
func formatExt(format string) string {
	if format == "raw" {
		return ""
	}
	return format
}

// lastPathSegment returns the final /-separated segment of a URL.
func lastPathSegment(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:]
	}
	return u
}
