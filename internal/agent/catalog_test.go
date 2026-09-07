package agent

import (
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestCatalog_AnEmptyListIsNotAnEmptyCatalog(t *testing.T) {
	// The rule that used to live on the chat record. session/load omits the
	// catalog routinely (KAS resolves it asynchronously), and modes have no repair
	// channel — a live config_option_update carries models, never modes — so a
	// write-the-zeros would leave the picker empty for the rest of the session.
	seededModes := []vibekit.SessionMode{{ID: "spec", Name: "Spec"}}
	seededModels := []vibekit.SessionModel{{ID: "m1", Name: "One"}}
	c := &Catalog{}
	c.SetModes(seededModes)
	c.SetModels(seededModels)

	for _, modes := range [][]vibekit.SessionMode{nil, {}} {
		if c.SetModes(modes) {
			t.Errorf("SetModes(%v) reported a change, want false", modes)
		}
	}
	for _, models := range [][]vibekit.SessionModel{nil, {}} {
		if c.SetModels(models) {
			t.Errorf("SetModels(%v) reported a change, want false", models)
		}
	}

	if !slices.Equal(c.Modes(), seededModes) {
		t.Errorf("Modes() = %v, want the seeded %v", c.Modes(), seededModes)
	}
	if !slices.Equal(c.Models(), seededModels) {
		t.Errorf("Models() = %v, want the seeded %v", c.Models(), seededModels)
	}
}

func TestCatalog_ReportsAChangeOnlyWhenSomethingChanged(t *testing.T) {
	// The caller's contract: the chat store only persists and broadcasts on a
	// change, so a repeated frame must answer false.
	modes := []vibekit.SessionMode{{ID: "spec", Name: "Spec"}}
	c := &Catalog{}

	if !c.SetModes(modes) {
		t.Error("the first SetModes reported no change, want true")
	}
	if c.SetModes(slices.Clone(modes)) {
		t.Error("an identical SetModes reported a change, want false")
	}
	if !c.SetModes([]vibekit.SessionMode{{ID: "spec", Name: "Specification"}}) {
		t.Error("a renamed mode reported no change, want true: the NAME is what the picker renders")
	}
}

func TestCatalog_ReturnsACopy(t *testing.T) {
	// The caller is a JSON encoder or a picker; neither may reach the holder's
	// slice. SessionMode holds only strings, so one level of copy is the whole
	// value.
	c := &Catalog{}
	c.SetModes([]vibekit.SessionMode{{ID: "spec", Name: "Spec"}})

	got := c.Modes()
	got[0].Name = "mutated by the caller"

	if c.Modes()[0].Name != "Spec" {
		t.Errorf("Modes()[0].Name = %q after a caller mutated its copy, want %q",
			c.Modes()[0].Name, "Spec")
	}
}

func TestCatalog_SeedingIsNotSharedWithTheCaller(t *testing.T) {
	// The other direction: the holder must not alias the slice it was handed, or a
	// bridge reusing its own buffer would rewrite the catalog behind it.
	modes := []vibekit.SessionMode{{ID: "spec", Name: "Spec"}}
	c := &Catalog{}
	c.SetModes(modes)

	modes[0].Name = "mutated by the writer"

	if c.Modes()[0].Name != "Spec" {
		t.Errorf("Modes()[0].Name = %q after the writer mutated its own slice, want %q",
			c.Modes()[0].Name, "Spec")
	}
}

func TestCatalog_DefaultEffortFor(t *testing.T) {
	c := &Catalog{}
	c.SetModels([]vibekit.SessionModel{
		{ID: "m1", DefaultEffortLevel: "high"},
		{ID: "m2"},
	})

	tests := map[string]string{
		"m1": "high",
		"m2": "",
		"m9": "",
	}
	for model, want := range tests {
		if got := c.DefaultEffortFor(model); got != want {
			t.Errorf("DefaultEffortFor(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestCatalog_ConcurrentReadersAndWriters(t *testing.T) {
	// One holder, many bridges: a session/new, a session/load and a live
	// config_option_update can all publish while /api/config-template reads.
	c := &Catalog{}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			c.SetModes([]vibekit.SessionMode{{ID: "m", Name: string(rune('a' + i))}})
			c.SetModels([]vibekit.SessionModel{{ID: "m", Name: string(rune('a' + i))}})
		})
		wg.Go(func() {
			_ = c.Modes()
			_ = c.Models()
			_ = c.DefaultEffortFor("m")
		})
	}
	wg.Wait()

	if len(c.Modes()) != 1 || len(c.Models()) != 1 {
		t.Errorf("Modes()=%v Models()=%v, want one entry each", c.Modes(), c.Models())
	}
}
