package connector

import (
	"errors"
	"slices"
	"testing"
	"time"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tgerr"
)

// fakeUsers answers like users.getUsers: the whole request fails if any id in it is invalid.
type fakeUsers struct {
	bad      map[int64]bool
	floodAt  int // fail with a flood wait on this request number (1-based), 0 for never
	requests int
	asked    [][]int64
}

func (f *fakeUsers) fetch(chunk []int64) ([]int64, error) {
	f.requests++
	f.asked = append(f.asked, slices.Clone(chunk))
	if f.floodAt != 0 && f.requests == f.floodAt {
		return nil, tgerr.New(420, "FLOOD_WAIT_7")
	}
	for _, id := range chunk {
		if f.bad[id] {
			return nil, tgerr.New(400, "USER_ID_INVALID")
		}
	}
	return slices.Clone(chunk), nil
}

func seq(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

func sorted(s []int64) []int64 {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestFetchIsolatingBad(t *testing.T) {
	t.Run("all good is one request", func(t *testing.T) {
		f := &fakeUsers{}
		res, bad, wait, err := fetchIsolatingBad(seq(100), presenceBisectBudget, f.fetch)
		if len(res) != 100 || len(bad) != 0 || wait != 0 || err != nil || f.requests != 1 {
			t.Fatalf("res=%d bad=%v wait=%v err=%v requests=%d", len(res), bad, wait, err, f.requests)
		}
	})
	t.Run("one bad in the middle", func(t *testing.T) {
		f := &fakeUsers{bad: map[int64]bool{50: true}}
		res, bad, _, err := fetchIsolatingBad(seq(100), presenceBisectBudget, f.fetch)
		if err != nil || !slices.Equal(bad, []int64{50}) || len(res) != 99 || slices.Contains(res, 50) {
			t.Fatalf("res=%d bad=%v err=%v", len(res), bad, err)
		}
		if f.requests > 15 {
			t.Fatalf("one bad user among 100 took %d requests, bound is 15", f.requests)
		}
	})
	t.Run("several bad", func(t *testing.T) {
		f := &fakeUsers{bad: map[int64]bool{3: true, 50: true, 99: true}}
		res, bad, _, err := fetchIsolatingBad(seq(100), 100, f.fetch)
		if err != nil || !slices.Equal(sorted(bad), []int64{3, 50, 99}) || len(res) != 97 {
			t.Fatalf("res=%d bad=%v err=%v", len(res), bad, err)
		}
	})
	t.Run("all bad, with room", func(t *testing.T) {
		bad := map[int64]bool{}
		for _, id := range seq(8) {
			bad[id] = true
		}
		f := &fakeUsers{bad: bad}
		res, gotBad, _, err := fetchIsolatingBad(seq(8), 100, f.fetch)
		if err != nil || len(res) != 0 || len(gotBad) != 8 {
			t.Fatalf("res=%v bad=%v err=%v", res, gotBad, err)
		}
	})
	t.Run("budget stops the splitting and blames nobody undecided", func(t *testing.T) {
		bad := map[int64]bool{}
		for _, id := range seq(100) {
			bad[id] = true
		}
		f := &fakeUsers{bad: bad}
		_, gotBad, _, err := fetchIsolatingBad(seq(100), 10, f.fetch)
		if err != nil || f.requests != 10 || len(gotBad) >= 100 {
			t.Fatalf("requests=%d bad=%d err=%v", f.requests, len(gotBad), err)
		}
	})
	t.Run("flood wait stops at once", func(t *testing.T) {
		f := &fakeUsers{bad: map[int64]bool{50: true}, floodAt: 2}
		res, bad, wait, err := fetchIsolatingBad(seq(100), presenceBisectBudget, f.fetch)
		if wait != 7*time.Second || err != nil || f.requests != 2 || len(bad) != 0 {
			t.Fatalf("wait=%v err=%v requests=%d bad=%v res=%d", wait, err, f.requests, bad, len(res))
		}
	})
	t.Run("connection errors are not blamed on users", func(t *testing.T) {
		calls := 0
		_, bad, _, err := fetchIsolatingBad(seq(10), presenceBisectBudget, func([]int64) ([]int64, error) {
			calls++
			return nil, errors.New("connection reset")
		})
		if err == nil || calls != 1 || len(bad) != 0 {
			t.Fatalf("err=%v calls=%d bad=%v", err, calls, bad)
		}
	})
}

func TestFetchUnskippedRemembersBadUsers(t *testing.T) {
	var skips badUserSkips
	now := time.Unix(1_700_000_000, 0)
	f := &fakeUsers{bad: map[int64]bool{4: true}}
	res, _, err := fetchUnskipped(seq(10), &skips, now, f.fetch)
	if err != nil || len(res) != 9 {
		t.Fatalf("res=%v err=%v", res, err)
	}

	// The next round must not even ask about the bad user, so one request is enough.
	f = &fakeUsers{bad: map[int64]bool{4: true}}
	res, _, err = fetchUnskipped(seq(10), &skips, now.Add(presenceBadUserSkip-time.Second), f.fetch)
	if err != nil || len(res) != 9 || f.requests != 1 || slices.Contains(f.asked[0], 4) {
		t.Fatalf("res=%v err=%v requests=%d asked=%v", res, err, f.requests, f.asked)
	}

	// Once the skip is over the user is tried again.
	f = &fakeUsers{}
	res, _, err = fetchUnskipped(seq(10), &skips, now.Add(presenceBadUserSkip), f.fetch)
	if err != nil || len(res) != 10 || f.requests != 1 {
		t.Fatalf("res=%v err=%v requests=%d", res, err, f.requests)
	}
}
