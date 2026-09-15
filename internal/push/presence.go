package push

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/sse"
	"github.com/cplieger/vibekit/internal/liveness"
)

// Presence folds the hub's connected/disconnected feed and the client's keepalive
// acknowledgements into one verdict per client tag; Gone is what the send filter
// reads. The acknowledgement is the input the socket cannot supply: a locked
// phone's kernel keeps acknowledging while its page does not run, and a half-open
// peer stays connected for the kernel's whole retransmission budget.
//
// Safe for concurrent use.
type Presence struct {
	now     func() time.Time
	rows    map[string]*presenceRow
	mu      sync.Mutex
	alive   uint64
	expired uint64
}

// presenceRow is one tag's fold. A row is created by whichever feed arrives
// first, since the first acknowledgement can race the hook's connected.
type presenceRow struct {
	lastAliveAt time.Time
	// leftAt is when connected last fell to zero; the zero time while the tag has
	// a connection, or has never had one.
	leftAt    time.Time
	connected int
	// gone is the last verdict recorded, so a transition is counted once.
	gone bool
}

// PresenceRow is one tag's fold as the test-only probe reports it.
type PresenceRow struct {
	LastAliveAt time.Time
	Tag         string
	Connected   int
	Gone        bool
}

// NewPresence returns an empty table. Its windows are liveness.AliveWindow and
// liveness.ReconnectDelay, read at each verdict rather than copied in.
func NewPresence() *Presence {
	return &Presence{now: time.Now, rows: make(map[string]*presenceRow)}
}

// Observe folds one hub event. Every disconnected cause is one departure whatever
// ended the socket: closed, dead, evicted, shutdown and hook_failed each follow
// exactly one connected, so the count balances only if all of them decrement.
// An event with no tag belongs to a client that presented none and is dropped.
func (p *Presence) Observe(ev *sse.PresenceEvent) {
	if ev.Tag == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	row := p.row(ev.Tag)
	switch ev.Kind {
	case sse.PresenceConnected:
		row.connected++
		row.leftAt = time.Time{}
		// The hello just completed a request round trip, at least as good a receipt
		// as an acknowledgement; without it a fresh row would read gone for up to
		// one beat and an event in that beat would be pushed to a present profile.
		if now.After(row.lastAliveAt) {
			row.lastAliveAt = now
		}
	case sse.PresenceDisconnected:
		if row.connected > 0 {
			row.connected--
		}
		if row.connected == 0 {
			row.leftAt = now
		}
	default:
		return
	}
	p.judge(row, now)
	p.sweep(now)
}

// Alive records one keepalive acknowledgement for tag.
func (p *Presence) Alive(tag string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	row := p.row(tag)
	row.lastAliveAt = now
	p.judge(row, now)
	p.sweep(now)
}

// Gone reports whether no page behind tag is receiving the stream: an unseen tag,
// a tag whose last connection departed more than one retry interval ago, or a
// connected tag silent for longer than the alive window.
func (p *Presence) Gone(tag string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	row, ok := p.rows[tag]
	if !ok {
		return true
	}
	return p.judge(row, p.now())
}

// Rows returns every row with its current verdict, sorted by tag.
func (p *Presence) Rows() []PresenceRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	out := make([]PresenceRow, 0, len(p.rows))
	for tag, row := range p.rows {
		out = append(out, PresenceRow{
			Tag:         tag,
			Connected:   row.connected,
			LastAliveAt: row.lastAliveAt,
			Gone:        p.judge(row, now),
		})
	}
	slices.SortFunc(out, func(a, b PresenceRow) int { return strings.Compare(a.Tag, b.Tag) })
	return out
}

// Transitions reports how often a tag turned alive (present after being gone or
// unseen) and how often a connected tag expired (no acknowledgement within the
// alive window while its socket still read connected). The second is the count
// of how often the socket-only view would have been wrong. Both count OBSERVED
// flips: a row is judged only when something reads or feeds it, so a tag that
// expires and returns between two reads is not counted.
func (p *Presence) Transitions() (alive, expired uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive, p.expired
}

// row returns tag's row, creating it gone: a tag nobody has seen is gone.
func (p *Presence) row(tag string) *presenceRow {
	row, ok := p.rows[tag]
	if !ok {
		row = &presenceRow{gone: true}
		p.rows[tag] = row
	}
	return row
}

// judge computes the verdict and records a transition when it changed. Caller
// holds mu.
//
// A departure is provisional for one retry interval: a reconnecting client aborts
// its old connection and opens the new one within it, and reading the tag gone in
// between would count a spurious absence for a page that never left. A tag whose
// count is zero and has never been connected (a row an acknowledgement created)
// is gone at once.
func (p *Presence) judge(row *presenceRow, now time.Time) bool {
	silent := now.Sub(row.lastAliveAt) > liveness.AliveWindow
	left := row.connected == 0 &&
		(row.leftAt.IsZero() || now.Sub(row.leftAt) >= liveness.ReconnectDelay)
	gone := silent || left
	if gone == row.gone {
		return gone
	}
	row.gone = gone
	switch {
	case !gone:
		p.alive++
	case silent && row.connected > 0:
		p.expired++
	}
	return gone
}

// sweep drops every row with no connection whose last acknowledgement is older
// than the alive window. A zero-count row is not dropped at the disconnect,
// because an acknowledgement in flight when the socket closed would recreate a
// row nothing deletes; one window later both clauses already read it gone, so
// dropping it changes no verdict. Caller holds mu.
func (p *Presence) sweep(now time.Time) {
	for tag, row := range p.rows {
		if row.connected == 0 && now.Sub(row.lastAliveAt) > liveness.AliveWindow {
			delete(p.rows, tag)
		}
	}
}
