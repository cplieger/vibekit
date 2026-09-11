package agent

// The connect handshake's two projections are READ while the state under them is
// WRITTEN: busyChatIDs takes every lifecycle's mutex under the registry's, and
// liveRunRows projects the lease store while runs are granted and released.
// hasOpenTurn joins them because its two reads must share ONE hold. None of the three
// is reached from another concurrent test in this package, so without this one the
// detector has nothing to exercise at any of them and a read moved outside its lock
// would ship green.

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The invariant that holds WITHOUT the detector too, so the test is not merely a
// harness: a projection may report any SUBSET of what is live, because it samples a
// set that is moving under it — but never a value nothing minted, which is what a torn
// read or a walk over a foreign map would produce.
//
// Bounded by the connects rather than by a lap count: the mutation has to still be in
// flight while streamInitialState assembles both lists, and a lap count fast enough to
// be cheap finishes before the first connect is written.
func TestConnectProjections_ReadConcurrentlyWithTheirOwnMutation(t *testing.T) {
	rt := newBudgetRuntime(t)
	const fixtureChats, fixtureRuns, readers = 4, 4, 3

	chatIDs := make([]vibekit.ChatID, 0, fixtureChats)
	knownChat := make(map[vibekit.ChatID]bool, fixtureChats)
	for i := range fixtureChats {
		id := vibekit.ChatID(fmt.Sprintf("c-conc-%02d", i))
		chatIDs = append(chatIDs, id)
		knownChat[id] = true
		rt.bridge.mgr.orInsert(id)
	}
	knownRun := make(map[string]bool, fixtureRuns)
	runIDs := make([]string, 0, fixtureRuns)
	for i := range fixtureRuns {
		id := fmt.Sprintf("wf-conc-%02d", i)
		runIDs = append(runIDs, id)
		knownRun[id] = true
	}

	// One chat holds an OPEN turn for the whole test, so the walk reads a live turn's
	// facts rather than only the reservation half of the predicate.
	if rt.coord.StartTurn(t.Context(), chatIDs[0], vibekit.TurnSourcePrompt) == 0 {
		t.Fatal("StartTurn refused the fixture's open turn, so busyChatIDs never reaches " +
			"openFactsLocked and only the reservation half of the walk is exercised")
	}

	store := rt.runs.leaseStore()
	ctx := t.Context()
	stop := make(chan struct{})
	badChat := make(chan vibekit.ChatID, 1)
	badRun := make(chan string, 1)
	var wg sync.WaitGroup

	// Writers: the reservation pair writes lc.reserved and lc.reservedSource, the two
	// fields both registry reads below consult.
	for _, id := range chatIDs[1:] {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if rt.coord.TryReserveTurn(id, vibekit.TurnSourcePrompt) {
					rt.coord.ReleaseTurnReservation(id)
				}
			}
		})
	}
	for _, id := range runIDs {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = store.Put(ctx, &runlease.Lease{
					WorkflowID: id,
					ChatID:     string(chatIDs[0]),
					Recipe:     "r",
					Origin:     runlease.OriginManual,
				})
				_ = store.Release(ctx, id)
			}
		})
	}

	// Readers. Reported through channels rather than t.Errorf: a Fatal off the test's
	// own goroutine ends the WRONG goroutine, and Errorf from many readers would name
	// one cause several times.
	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range rt.coord.turns.busyChatIDs() {
					if !knownChat[id] {
						select {
						case badChat <- id:
						default:
						}
					}
				}
				for _, r := range rt.runs.liveRunRows() {
					if !knownRun[r.WorkflowID] {
						select {
						case badRun <- r.WorkflowID:
						default:
						}
					}
				}
				for _, id := range chatIDs {
					_ = rt.coord.turns.hasOpenTurn(id)
				}
			}
		})
	}

	// The connect region, on the test's own goroutine so its assertions may Fatal, and
	// TWICE so the second one lands with every writer above still running.
	for range 2 {
		if p := connectPayload(t, rt, "?snapshot="+snapshotNone); !p.BusyStated {
			t.Error("a connect taken while the lifecycle set is mutating withholds its busy " +
				"list, so the client retracts nothing and a stale `thinking` survives")
		}
	}
	close(stop)
	wg.Wait()

	select {
	case id := <-badChat:
		t.Errorf("busyChatIDs reported chat %q, which no fixture minted", id)
	default:
	}
	select {
	case id := <-badRun:
		t.Errorf("liveRunRows reported run %q, which no fixture minted", id)
	default:
	}
}
