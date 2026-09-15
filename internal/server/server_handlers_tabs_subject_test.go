package server

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The tabs envelope carries the collection version a second time, as the `tabs`
// digest stamp with the hub epoch, so the client's version map observes it on
// commit without a second counter existing anywhere.
func TestTabs_SubjectIsTheCollectionVersionWithTheEpoch(t *testing.T) {
	s, st := newTabsServer(t)
	s.agent = &fakeEngine{}
	if _, _, _, err := st.Open(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindSettings}); err != nil {
		t.Fatalf("open: %v", err)
	}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	want := vibekit.SubjectStamp{Kind: "tabs", Version: strconv.FormatUint(got.Version, 10), Epoch: "fake-epoch"}
	if got.Subject == nil || *got.Subject != want {
		t.Errorf("subject = %+v, want %+v", got.Subject, want)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1: the stamp above must spell a real mutation", got.Version)
	}
}

// Without a runtime there is no hub and no epoch, so no stamp: a stamp whose epoch
// is empty would be refused by every client anyway.
func TestTabs_NoRuntimeMeansNoSubject(t *testing.T) {
	s, _ := newTabsServer(t)
	got, _ := getTabs(t, s)
	if got.Subject != nil {
		t.Errorf("subject = %+v, want nil with no runtime wired", *got.Subject)
	}
}
