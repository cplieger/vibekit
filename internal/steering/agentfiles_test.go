package steering

import (
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// fakeDirEntry is a directory listing entry the test controls completely,
// including names no filesystem would accept. The rule reads only Name and
// IsDir, so Type and Info are the minimum that satisfies the interface.
type fakeDirEntry struct {
	name  string
	isDir bool
}

func (e fakeDirEntry) Name() string { return e.name }
func (e fakeDirEntry) IsDir() bool  { return e.isDir }
func (e fakeDirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e fakeDirEntry) Info() (fs.FileInfo, error) { return fakeFileInfo{e}, nil }

type fakeFileInfo struct{ e fakeDirEntry }

func (i fakeFileInfo) Name() string       { return i.e.name }
func (i fakeFileInfo) Size() int64        { return 0 }
func (i fakeFileInfo) Mode() fs.FileMode  { return i.e.Type() }
func (i fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeFileInfo) IsDir() bool        { return i.e.isDir }
func (i fakeFileInfo) Sys() any           { return nil }

func entries(names ...string) []fs.DirEntry {
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		out = append(out, fakeDirEntry{name: n})
	}
	return out
}

// agentDoorFixture is the listing every door test uses. Its twin lives in
// internal/server/kiro_agent_doors_test.go, which asserts the two REST scans
// resolve it the way DedupeAgentFiles does; TestFindRepoAgents_AgreesWithTheRule
// below does the same for the generator's door.
//
// Sorted, because both production readers (fs.ReadDir and os.ReadDir) sort by
// name, so this is the order the doors actually see.
var agentDoorFixture = []string{
	".hidden.md",
	".md",
	"README.txt",
	"deploy.json",
	"notes.md",
	"reviewer.json",
	"reviewer.md",
}

// TestDedupeAgentFiles is the ONE statement of what an agents/ listing means.
// Every door reads it through this function, so these expectations are the whole
// contract and no door restates them.
func TestDedupeAgentFiles(t *testing.T) {
	tests := map[string]struct {
		in   []fs.DirEntry
		want []AgentFile
		why  string
	}{
		"a pair collapses to the markdown": {
			in:   entries("reviewer.json", "reviewer.md"),
			want: []AgentFile{{Base: "reviewer", File: "reviewer.md"}},
			why:  "the .md is what carries the front-matter, so it is the spelling that can describe itself",
		},
		"the markdown wins whichever order it is listed in": {
			in:   entries("reviewer.md", "reviewer.json"),
			want: []AgentFile{{Base: "reviewer", File: "reviewer.md"}},
			why:  "directory order is not a preference; a listing that happens to name the .md first must resolve the same",
		},
		"a json-only agent is listed under its json": {
			in:   entries("deploy.json"),
			want: []AgentFile{{Base: "deploy", File: "deploy.json"}},
			why:  "dropping it would hide an agent that exists",
		},
		"a markdown-only agent is listed": {
			in:   entries("notes.md"),
			want: []AgentFile{{Base: "notes", File: "notes.md"}},
		},
		"a third spelling of the same base does not displace the markdown": {
			in:   entries("reviewer.md", "reviewer.json", "reviewer.md"),
			want: []AgentFile{{Base: "reviewer", File: "reviewer.md"}},
			why:  "the only upgrade is json -> md, so nothing can overwrite a chosen .md",
		},
		"a directory is not an agent": {
			in:   []fs.DirEntry{fakeDirEntry{name: "archive.md", isDir: true}},
			want: nil,
			why:  "a directory named like a doc would produce an entry no reader can open",
		},
		"a dot-prefixed file is not an agent": {
			in:   entries(".hidden.md", "._deploy.json"),
			want: nil,
			why:  "an AppleDouble or a hidden draft is not authored inventory, and its base name renders as junk",
		},
		"a bare extension is not an agent": {
			in:   entries(".md", ".json"),
			want: nil,
			why:  "its base name is empty; the dot rule already covers it, and an empty-named row is unopenable",
		},
		"an unrelated extension is not an agent": {
			in:   entries("README.txt", "config.yaml", "noext"),
			want: nil,
		},
		"a NUL in the name is refused": {
			in:   entries("deploy\x00.json"),
			want: nil,
			why:  "two of the three doors build a path the client is handed from this name",
		},
		"order is the listing's, not the map's": {
			in: entries("zeta.md", "alpha.json", "middle.md"),
			want: []AgentFile{
				{Base: "zeta", File: "zeta.md"},
				{Base: "alpha", File: "alpha.json"},
				{Base: "middle", File: "middle.md"},
			},
			why: "a cap applied at the call site takes a prefix, so the prefix has to be stable",
		},
		"the shared door fixture": {
			in: entries(agentDoorFixture...),
			want: []AgentFile{
				{Base: "deploy", File: "deploy.json"},
				{Base: "notes", File: "notes.md"},
				{Base: "reviewer", File: "reviewer.md"},
			},
			why: "every door test resolves this listing; this is the answer they are all checked against",
		},
		"an empty listing yields nothing": {
			in:   nil,
			want: nil,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := DedupeAgentFiles(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("DedupeAgentFiles = %+v (len %d), want %+v (len %d) — %s",
					got, len(got), tc.want, len(tc.want), tc.why)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %+v, want %+v — %s", i, got[i], tc.want[i], tc.why)
				}
			}
		})
	}
}

// TestFindRepoAgents_AgreesWithTheRule is the generator's door. It asserts
// agreement with DedupeAgentFiles rather than restating the expectations, so the
// rule's table above stays the only place they live — which is the whole point of
// there being one rule.
func TestFindRepoAgents_AgreesWithTheRule(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".kiro", "agents")
	for _, name := range agentDoorFixture {
		if name == ".md" {
			// A file literally named ".md" is legal on disk and the rule refuses it;
			// written here so the door sees the same listing as its twins.
			mustWriteFile(t, filepath.Join(dir, name), "")
			continue
		}
		mustWriteFile(t, filepath.Join(dir, name), "# "+name+"\n")
	}

	want := DedupeAgentFiles(entries(agentDoorFixture...))
	got := findRepoAgents(repo)

	if len(got) != len(want) {
		t.Fatalf("findRepoAgents = %+v (len %d), want the rule's %+v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Base || got[i].Filename != want[i].File {
			t.Errorf("findRepoAgents[%d] = {Name:%q Filename:%q}, the rule says {Base:%q File:%q}",
				i, got[i].Name, got[i].Filename, want[i].Base, want[i].File)
		}
	}
}
