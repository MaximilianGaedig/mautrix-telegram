// mautrix-telegram - A Matrix-Telegram puppeting bridge.
// Copyright (C) 2026 Maximilian Gaedig
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"sync"
	"time"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tgerr"
)

const (
	// How long a user that Telegram rejected on its own is left out of the polls. Their access hash is
	// stale or the account is gone, and neither fixes itself within a poll round.
	presenceBadUserSkip = time.Hour
	// The most requests one status poll may spend, on top of its one request per batch, on finding the
	// users Telegram rejects. Finding one among 100 takes 2*ceil(log2(100)) = 14 of them, so this
	// settles two a poll; whoever is left undecided is asked about again the next time their batch comes
	// up, with the ones found by then already left out, instead of hammering Telegram now.
	presenceBisectExtra = 30
)

// rejectsInput reports whether err is Telegram refusing the request for what was asked (a 400: a stale
// access hash, a deleted account). Only that says anything about the inputs: a 500, a timeout or a lost
// authorisation fails whoever is asked about, and splitting on those would blame users at random.
func rejectsInput(err error) bool {
	return tgerr.IsCode(err, 400)
}

// fetchIsolatingBad asks fetch for all items, and when Telegram rejects the request it splits the
// batch in two and asks for each half, down to single items, so that one invalid input costs only
// itself instead of the whole batch (users.getUsers fails entirely if any one input is invalid).
//
// Cost: a batch of n items with k bad ones takes at most 1 + 2*k*ceil(log2(n)) requests, since a
// failing request is always followed by its two halves and only the halves that hold a bad item fail
// again; budget caps it in any case. Items still undecided when the budget runs out are returned in
// neither results nor bad.
//
// It stops at once, returning the wait, on a flood wait, and with the error on anything that isn't
// Telegram rejecting the inputs (see rejectsInput).
func fetchIsolatingBad[T, R any](items []T, budget int, fetch func([]T) ([]R, error)) (results []R, bad []T, wait time.Duration, err error) {
	requests := 0
	var run func(chunk []T) bool
	run = func(chunk []T) bool {
		if len(chunk) == 0 {
			return true
		}
		if requests >= budget {
			return false
		}
		requests++
		got, fetchErr := fetch(chunk)
		if fetchErr == nil {
			results = append(results, got...)
			return true
		}
		if d, isFlood := tgerr.AsFloodWait(fetchErr); isFlood {
			// Never zero: the caller tells a flood wait from success by the wait alone.
			wait = max(d, time.Second)
			return false
		}
		if !rejectsInput(fetchErr) {
			err = fetchErr
			return false
		}
		if len(chunk) == 1 {
			bad = append(bad, chunk[0])
			return true
		}
		mid := len(chunk) / 2
		return run(chunk[:mid]) && run(chunk[mid:])
	}
	run(items)
	return
}

// badUserSkips remembers the users Telegram rejected on their own, so they are not asked about again
// every round. The zero value is ready to use.
type badUserSkips struct {
	mu    sync.Mutex
	until map[int64]time.Time
}

func (s *badUserSkips) skipped(id int64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.until[id]
	if ok && !now.Before(until) {
		delete(s.until, id)
		return false
	}
	return ok
}

func (s *badUserSkips) add(id int64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.until == nil {
		s.until = map[int64]time.Time{}
	}
	// Someone who has left the groups is never asked about again, so their entry would never be
	// looked up and dropped. Clearing what has run out here keeps the list to the users rejected
	// within the last presenceBadUserSkip.
	for other, until := range s.until {
		if !now.Before(until) {
			delete(s.until, other)
		}
	}
	s.until[id] = now.Add(presenceBadUserSkip)
}

// fetchUnskipped asks fetch about ids in batches of batchSize, leaving out the users on the skip list
// and adding the ones Telegram rejects on their own to it. It returns those newly rejected users too.
//
// Cost: one request per batch, plus at most extra more in total for splitting the batches Telegram
// rejects, so ceil(len(ids)/batchSize) + extra requests at the very most. It stops at the first flood
// wait or error, returning what it has by then.
func fetchUnskipped[R any](
	ids []int64, batchSize, extra int, skips *badUserSkips, now time.Time, fetch func([]int64) ([]R, error),
) (results []R, rejected []int64, wait time.Duration, err error) {
	live := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !skips.skipped(id, now) {
			live = append(live, id)
		}
	}
	for start := 0; start < len(live) && wait == 0 && err == nil; start += batchSize {
		requests := 0
		var got []R
		var bad []int64
		got, bad, wait, err = fetchIsolatingBad(live[start:min(start+batchSize, len(live))], 1+extra, func(chunk []int64) ([]R, error) {
			requests++
			return fetch(chunk)
		})
		extra -= requests - 1
		results = append(results, got...)
		for _, id := range bad {
			skips.add(id, now)
		}
		rejected = append(rejected, bad...)
	}
	return
}
