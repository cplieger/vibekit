package chat

// Cross-chat search: the History page's box, answering "which conversation was
// that in".
//
// Deliberately a DIFFERENT question from the in-chat Ctrl-F, which stays scoped
// to the chat you are reading (user decision). So this returns CHATS ranked by
// how well they match, each with its single best line, rather than a flat list
// of every hit — the answer is a conversation to open, not a position to jump
// to. Once the chat is open, its own search takes over.
//
// Lexical and index-free, consistent with SearchChat: the substrate is the same
// per-chat scan fanned out over the existing bounded-parallel reader.

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

// The scan window, result cap and title boost are KiroCrew's
// `_SEARCH_SCAN_WINDOW`, `search_sessions(limit=50)` and `_TITLE_BOOST`
// (src/kiro_crew/history.py), adopted with their values rather than guessed --
// it ships against the same kiro-cli and its ranking is documented reasoning
// rather than a hunch.
const (
	// maxChatsScanned bounds one search: a no-index fan-out reads every chat
	// file, so the newest N are scanned and older ones reported as unscanned.
	maxChatsScanned = 500
	// maxChatResults caps the returned list. Past this a search is not a search.
	maxChatResults = 50
	// titleBoost multiplies TITLE hits. Titles are short and intentional, so a
	// hit there is much stronger evidence than a mention in the body, and it is
	// the one signal a user can predict.
	titleBoost = 10.0
)

// searchWorkers matches readHeadersParallel: the bound is disk, not CPU.
const searchWorkers = 8

// Match is one chat that matched, with the evidence for showing it.
type Match struct {
	Name string     `json:"name"`
	ID   api.ChatID `json:"id"`
	// Best is the highest-scoring hit in this chat: the line the result row
	// shows, and the position the client can jump to after opening.
	Best SearchHit `json:"best"`
	// Hits is how many matches the chat holds, so a row can say "and 11 more".
	Hits int `json:"hits"`
	// Score ranks the row; see scoreChat for what it balances.
	Score float64 `json:"score"`
	// UpdatedAt breaks ties toward the more recent conversation.
	UpdatedAt int64 `json:"updated_at"`
}

// SearchAllResult is GET /api/chats/search's reply.
type SearchAllResult struct {
	Matches []Match `json:"matches"`
	// Scanned is how many chats were read, and Truncated says the scan hit
	// maxChatsScanned — so the UI can be honest that older chats were not read
	// rather than implying an empty result means "not there".
	Scanned   int  `json:"scanned"`
	Truncated bool `json:"truncated"`
}

// SearchAll runs the per-chat search across every chat, newest first.
func (s *Store) SearchAll(ctx context.Context, query string) SearchAllResult {
	if strings.TrimSpace(query) == "" {
		return SearchAllResult{Matches: []Match{}}
	}
	entries, truncated := s.newestEntries(ctx)
	if len(entries) == 0 {
		return SearchAllResult{Matches: []Match{}, Truncated: truncated}
	}

	found := make([]Match, len(entries))
	boundedParallel(ctx, entries, searchWorkers, func(idx int, ce chatEntry) {
		found[idx] = searchOneChat(ce, query)
	})

	matches := make([]Match, 0, 16)
	for i := range found {
		if found[i].ID != "" {
			matches = append(matches, found[i])
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].UpdatedAt > matches[j].UpdatedAt
	})
	if len(matches) > maxChatResults {
		matches = matches[:maxChatResults]
	}
	return SearchAllResult{Matches: matches, Scanned: len(entries), Truncated: truncated}
}

// searchOneChat scans one chat file, returning a zero Match when it does not
// match or cannot be read.
func searchOneChat(ce chatEntry, query string) Match {
	c, err := readChatFile(ce.path, "chat "+ce.id)
	if err != nil {
		// A concurrent delete is normal and not worth a line; anything else
		// means a chat that exists was not searched, which the user should not
		// have to guess at.
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("chat search: skipping unreadable file", "chat_id", ce.id, "error", err)
		}
		return Match{}
	}
	hits := SearchChat(c.Messages, query)
	// A chat whose TITLE names the subject is a result even when its body never
	// repeats the word — dropping it on content hits alone would make the title
	// boost unreachable in exactly the case it exists for.
	titles := titleHits(c.Name, query)
	if len(hits) == 0 && titles == 0 {
		return Match{}
	}
	m := Match{
		Name:      c.Name,
		ID:        api.ChatID(c.ID),
		Hits:      len(hits),
		Score:     scoreChat(len(hits), titles, docChars(c.Messages), c.Name),
		UpdatedAt: c.UpdatedAt,
	}
	// A title-only match has no line to show; the row falls back to the name.
	if len(hits) > 0 {
		m.Best = bestHit(hits)
	}
	return m
}

// newestEntries lists chat files newest-first, capped at maxChatsScanned.
//
// Ordering by the file's own mtime rather than by reading each header: the point
// of the cap is to avoid reading every file, so the ordering cannot depend on
// having read them.
func (s *Store) newestEntries(ctx context.Context) (entries []chatEntry, truncated bool) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat search: unreadable dir", "dir", s.dir, "error", err)
		return nil, false
	}
	type stamped struct {
		ce   chatEntry
		mtim int64
	}
	all := make([]stamped, 0, len(des))
	for _, e := range des {
		name := e.Name()
		if !strings.HasSuffix(name, chatFileSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, chatFileSuffix)
		if !chatIDPattern(api.ChatID(id)) {
			continue
		}
		var mtim int64
		if fi, sErr := e.Info(); sErr == nil {
			mtim = fi.ModTime().UnixMilli()
		}
		all = append(all, stamped{
			ce:   chatEntry{id: id, path: filepath.Join(s.dir, name)},
			mtim: mtim,
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mtim > all[j].mtim })
	if len(all) > maxChatsScanned {
		all = all[:maxChatsScanned]
		truncated = true
	}
	out := make([]chatEntry, 0, len(all))
	for i := range all {
		if ctx.Err() != nil {
			break
		}
		out = append(out, all[i].ce)
	}
	return out, truncated
}

// bestHit picks the hit a result row shows: the earliest one, which is where the
// conversation first touches the subject and reads as the most explanatory.
func bestHit(hits []SearchHit) SearchHit {
	best := hits[0]
	for i := range hits {
		if hits[i].Turn < best.Turn {
			best = hits[i]
		}
	}
	return best
}

// scoreChat ranks a matching chat, using KiroCrew's formula verbatim:
//
//	score = title_hits*titleBoost + content_hits/sqrt(1 + docChars/1024)
//
// Two details are load-bearing and both differ from a naive count. The title
// term is a MULTIPLIER on the number of title hits, not a flag, so naming the
// subject twice counts twice. And the length normaliser divides by CHARACTER
// volume in KiB, not by message count -- one enormous message and fifty short
// ones are not the same amount of text to get a casual mention into, which is
// exactly the case the normaliser exists to discount. The `1 +` keeps a tiny
// chat from being divided by nearly zero.
//
// KiroCrew's own note records why this is not BM25: the `(1-b) + b*(dl/avgdl)`
// term needs corpus-wide stats, which would mean a second pass over every chat.
func scoreChat(contentHits, titleHitCount, docChars int, _ string) float64 {
	lengthNorm := math.Sqrt(1 + float64(docChars)/1024)
	return float64(titleHitCount)*titleBoost + float64(contentHits)/lengthNorm
}

// docChars is the chat's text volume, the normaliser's input.
func docChars(msgs []api.Message) int {
	n := 0
	for i := range msgs {
		n += len(msgs[i].Content)
		for j := range msgs[i].ToolCalls {
			n += len(msgs[i].ToolCalls[j].Output)
		}
	}
	return n
}

// titleHits counts case-insensitive occurrences of the query's free text in the
// chat name. Filter-only queries (`file:x`) never match a title.
func titleHits(name, query string) int {
	// parseSearchQuery already separates filters from free text, so the list
	// of filter keys lives in ONE place.
	text := strings.TrimSpace(parseSearchQuery(query).text)
	if text == "" {
		return 0
	}
	return strings.Count(strings.ToLower(name), strings.ToLower(text))
}
