package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// getTaskStatusesFixture is a real-shape _kiro/spec/getTaskStatuses result
// (verified against the live KAS wire): a nested tree with the markdown
// statuses, an optional task, and — on the tasks that have been executed —
// executionStatus + provenance ids + a PBT result. Tasks never executed omit
// those fields entirely (the sparse state).
const getTaskStatusesFixture = `{
  "tasks": [
    {
      "taskId": "1. Parent done",
      "markdownStatus": "completed",
      "isLeaf": false,
      "isOptional": false,
      "subTasks": [
        {"taskId": "1.1 Child done", "markdownStatus": "completed", "isLeaf": true, "isOptional": false, "subTasks": []},
        {"taskId": "1.2 Child todo", "markdownStatus": "not_started", "executionStatus": "aborted",
         "lastSessionId": "sess_abc", "lastExecutionId": "exec_1", "isLeaf": true, "isOptional": false, "subTasks": []}
      ]
    },
    {
      "taskId": "2. PBT task",
      "markdownStatus": "in_progress",
      "executionStatus": "running",
      "pbtResult": {"status": "failed", "failingExample": "n = 42"},
      "isLeaf": true,
      "isOptional": true,
      "subTasks": []
    }
  ]
}`

func TestConvertSpecTaskNodes(t *testing.T) {
	var res kasSpecTasksResult
	if err := json.Unmarshal([]byte(getTaskStatusesFixture), &res); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	tree := convertSpecTaskNodes(res.Tasks)

	if len(tree) != 2 {
		t.Fatalf("top-level tasks = %d, want 2", len(tree))
	}

	// Node 1: parent with two children, no execution metadata.
	p := tree[0]
	if p.TaskID != "1. Parent done" || p.MarkdownStatus != "completed" || p.IsLeaf || p.IsOptional {
		t.Errorf("node 1 = %+v", p)
	}
	if p.ExecutionStatus != "" || p.PBTResult != nil {
		t.Errorf("node 1 should have no execution metadata, got exec=%q pbt=%v", p.ExecutionStatus, p.PBTResult)
	}
	if len(p.SubTasks) != 2 {
		t.Fatalf("node 1 subtasks = %d, want 2", len(p.SubTasks))
	}
	// Child 1.2 carries an aborted execution + provenance.
	c := p.SubTasks[1]
	if c.TaskID != "1.2 Child todo" || c.ExecutionStatus != "aborted" ||
		c.LastSessionID != "sess_abc" || c.LastExecutionID != "exec_1" || !c.IsLeaf {
		t.Errorf("child 1.2 = %+v", c)
	}

	// Node 2: optional, running, with a failed PBT result.
	n2 := tree[1]
	if n2.MarkdownStatus != "in_progress" || n2.ExecutionStatus != "running" || !n2.IsOptional || !n2.IsLeaf {
		t.Errorf("node 2 = %+v", n2)
	}
	if n2.PBTResult == nil || n2.PBTResult.Status != "failed" || n2.PBTResult.FailingExample != "n = 42" {
		t.Errorf("node 2 pbt = %v, want {failed, n = 42}", n2.PBTResult)
	}
}

// TestConvertSpecTaskNodes_Empty pins that an empty/absent task list converts
// to a non-nil empty slice (so the client always decodes an array).
func TestConvertSpecTaskNodes_Empty(t *testing.T) {
	out := convertSpecTaskNodes(nil)
	if out == nil || len(out) != 0 {
		t.Errorf("convertSpecTaskNodes(nil) = %v, want non-nil empty slice", out)
	}
}

func TestSpecRelPath(t *testing.T) {
	got := specRelPath("my-feature", specDocRequirements)
	want := ".kiro/specs/my-feature/requirements.md"
	if got != want {
		t.Errorf("specRelPath = %q, want %q", got, want)
	}
}

func TestStatSpecDoc(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, specDocDesign), []byte("# Design\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !statSpecDoc(dir, specDocDesign) {
		t.Error("statSpecDoc(design) = false, want true (file exists)")
	}
	if statSpecDoc(dir, specDocRequirements) {
		t.Error("statSpecDoc(requirements) = true, want false (absent)")
	}
	// A directory named like a doc must not count as a present document.
	if err := os.Mkdir(filepath.Join(dir, specDocTasks), 0o750); err != nil {
		t.Fatal(err)
	}
	if statSpecDoc(dir, specDocTasks) {
		t.Error("statSpecDoc(tasks) = true for a directory, want false")
	}
}

// TestListSpecs_MissingDir pins that a workspace with no .kiro/specs directory
// yields an empty board (not an error).
func TestListSpecs_MissingDir(t *testing.T) {
	h := &Hub{lifecycle: &lifecyclePlane{workDir: t.TempDir()}}
	specs, err := h.listSpecs(context.Background())
	if err != nil {
		t.Fatalf("listSpecs on missing dir: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("specs = %d, want 0", len(specs))
	}
}

// TestListSpecs_Enumeration pins the pure enumeration path (document presence,
// workspace-relative paths, sorting, non-directory skipping). Specs here carry
// no tasks.md, so getTaskStatuses (which needs a live bridge) is never called.
func TestListSpecs_Enumeration(t *testing.T) {
	work := t.TempDir()
	specsRoot := filepath.Join(work, ".kiro", "specs")
	// beta: requirements + design, no tasks.md.
	mkdirAll(t, filepath.Join(specsRoot, "beta"))
	writeFile(t, filepath.Join(specsRoot, "beta", specDocRequirements), "# R\n")
	writeFile(t, filepath.Join(specsRoot, "beta", specDocDesign), "# D\n")
	// alpha: requirements only.
	mkdirAll(t, filepath.Join(specsRoot, "alpha"))
	writeFile(t, filepath.Join(specsRoot, "alpha", specDocRequirements), "# R\n")
	// A stray non-directory entry under specs/ must be skipped.
	writeFile(t, filepath.Join(specsRoot, "README.md"), "not a spec\n")

	h := &Hub{lifecycle: &lifecyclePlane{workDir: work}}
	specs, err := h.listSpecs(context.Background())
	if err != nil {
		t.Fatalf("listSpecs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %d, want 2 (alpha, beta); non-dir skipped", len(specs))
	}
	// Sorted by name.
	if specs[0].Name != "alpha" || specs[1].Name != "beta" {
		t.Errorf("order = [%s %s], want [alpha beta]", specs[0].Name, specs[1].Name)
	}
	alpha := specs[0]
	if !alpha.HasRequirements || alpha.HasDesign || alpha.HasTasks {
		t.Errorf("alpha flags = req:%v des:%v tasks:%v, want req-only", alpha.HasRequirements, alpha.HasDesign, alpha.HasTasks)
	}
	if alpha.RequirementsPath != ".kiro/specs/alpha/requirements.md" {
		t.Errorf("alpha requirements path = %q", alpha.RequirementsPath)
	}
	if alpha.Tasks == nil {
		t.Error("alpha.Tasks should be a non-nil empty slice")
	}
	beta := specs[1]
	if !beta.HasRequirements || !beta.HasDesign || beta.HasTasks {
		t.Errorf("beta flags = req:%v des:%v tasks:%v, want req+design", beta.HasRequirements, beta.HasDesign, beta.HasTasks)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
