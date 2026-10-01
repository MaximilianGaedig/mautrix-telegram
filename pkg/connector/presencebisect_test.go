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
	floodAt  int    // fail with a flood wait on this request number (1-based), 0 for never
	flood    string // the flood wait to fail with, FLOOD_WAIT_7 if empty
	requests int
	asked    [][]int64
}

func (f *fakeUsers) fetch(chunk []int64) ([]int64, error) {
	f.requests++
	f.asked = append(f.asked, slices.Clone(chunk))
	if f.floodAt != 0 && f.requests == f.floodAt {
		if f.flood == "" {
			return nil, tgerr.New(420, "FLOOD_WAIT_7")
		}
		return nil, tgerr.New(420, f.flood)
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
		res, bad, wait, err := fetchIsolatingBad(seq(100), 32, f.fetch)
		if len(res) != 100 || len(bad) != 0 || wait != 0 || err != nil || f.requests != 1 {
			t.Fatalf("res=%d bad=%v wait=%v err=%v requests=%d", len(res), bad, wait, err, f.requests)
		}
	})
	t.Run("one bad in the middle", func(t *testing.T) {
		f := &fakeUsers{bad: map[int64]bool{50: true}}
		res, bad, _, err := fetchIsolatingBad(seq(100), 32, f.fetch)
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
		if f.requests > 1+2*3*7 {
			t.Fatalf("three bad users among 100 took %d requests, bound is 43", f.requests)
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
		res, bad, wait, err := fetchIsolatingBad(seq(100), 32, f.fetch)
		if wait != 7*time.Second || err != nil || f.requests != 2 || len(bad) != 0 {
			t.Fatalf("wait=%v err=%v requests=%d bad=%v res=%d", wait, err, f.requests, bad, len(res))
		}
	})
	t.Run("flood wait keeps what was answered before it", func(t *testing.T) {
		// Request 1 (all) is rejected, 2 (first half) answers, 3 (second half) is the flood wait.
		f := &fakeUsers{bad: map[int64]bool{80: true}, floodAt: 3}
		res, bad, wait, err := fetchIsolatingBad(seq(100), 32, f.fetch)
		if wait != 7*time.Second || err != nil || f.requests != 3 || len(bad) != 0 || !slices.Equal(res, seq(50)) {
			t.Fatalf("wait=%v err=%v requests=%d bad=%v res=%d", wait, err, f.requests, bad, len(res))
		}
	})
	t.Run("flood wait without a duration still stops", func(t *testing.T) {
		f := &fakeUsers{floodAt: 1, flood: "FLOOD_WAIT_0"}
		_, _, wait, err := fetchIsolatingBad(seq(10), 32, f.fetch)
		if wait <= 0 || err != nil || f.requests != 1 {
			t.Fatalf("wait=%v err=%v requests=%d", wait, err, f.requests)
		}
	})
	t.Run("server errors are not blamed on users", func(t *testing.T) {
		// A 500 or a lost authorisation fails every request, whoever is in it.
		for _, rpcErr := range []*tgerr.Error{tgerr.New(500, "INTERNAL"), tgerr.New(401, "AUTH_KEY_UNREGISTERED"), tgerr.New(-503, "Timeout")} {
			calls := 0
			_, bad, wait, err := fetchIsolatingBad(seq(10), 32, func([]int64) ([]int64, error) {
				calls++
				return nil, rpcErr
			})
			if !errors.Is(err, rpcErr) || calls != 1 || len(bad) != 0 || wait != 0 {
				t.Fatalf("%v: err=%v calls=%d bad=%v wait=%v", rpcErr, err, calls, bad, wait)
			}
		}
	})
	t.Run("connection errors are not blamed on users", func(t *testing.T) {
		calls := 0
		_, bad, _, err := fetchIsolatingBad(seq(10), 32, func([]int64) ([]int64, error) {
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
	res, rejected, _, err := fetchUnskipped(seq(10), 100, 30, &skips, now, f.fetch)
	if err != nil || len(res) != 9 || !slices.Equal(rejected, []int64{4}) {
		t.Fatalf("res=%v rejected=%v err=%v", res, rejected, err)
	}

	// The next round must not even ask about the bad user, so one request is enough.
	f = &fakeUsers{bad: map[int64]bool{4: true}}
	res, _, _, err = fetchUnskipped(seq(10), 100, 30, &skips, now.Add(presenceBadUserSkip-time.Second), f.fetch)
	if err != nil || len(res) != 9 || f.requests != 1 || slices.Contains(f.asked[0], 4) {
		t.Fatalf("res=%v err=%v requests=%d asked=%v", res, err, f.requests, f.asked)
	}

	// Once the skip is over the user is tried again.
	f = &fakeUsers{}
	res, _, _, err = fetchUnskipped(seq(10), 100, 30, &skips, now.Add(presenceBadUserSkip), f.fetch)
	if err != nil || len(res) != 10 || f.requests != 1 {
		t.Fatalf("res=%v err=%v requests=%d", res, err, f.requests)
	}
}

func TestFetchUnskippedBatches(t *testing.T) {
	t.Run("all good is one request a batch", func(t *testing.T) {
		var skips badUserSkips
		f := &fakeUsers{}
		res, rejected, wait, err := fetchUnskipped(seq(250), 100, 30, &skips, time.Unix(1_700_000_000, 0), f.fetch)
		if err != nil || wait != 0 || len(rejected) != 0 || !slices.Equal(res, seq(250)) || f.requests != 3 {
			t.Fatalf("res=%d rejected=%v wait=%v err=%v requests=%d", len(res), rejected, wait, err, f.requests)
		}
		for i, want := range []int{100, 100, 50} {
			if len(f.asked[i]) != want {
				t.Fatalf("batch %d had %d users, want %d", i, len(f.asked[i]), want)
			}
		}
	})
	t.Run("the extra requests are shared by all batches", func(t *testing.T) {
		// One bad user in each of three batches: the first two are found for 14 extra requests each,
		// which leaves 2 for the third batch, not enough to find its bad user.
		var skips badUserSkips
		f := &fakeUsers{bad: map[int64]bool{50: true, 150: true, 250: true}}
		now := time.Unix(1_700_000_000, 0)
		res, rejected, _, err := fetchUnskipped(seq(300), 100, 30, &skips, now, f.fetch)
		if err != nil || f.requests > 3+30 || !slices.Equal(rejected, []int64{50, 150}) {
			t.Fatalf("requests=%d rejected=%v err=%v", f.requests, rejected, err)
		}
		if len(res) < 198 || slices.Contains(res, 250) {
			t.Fatalf("res=%d", len(res))
		}
		if skips.skipped(250, now) {
			t.Fatal("a user nobody got to ask about alone was put on the skip list")
		}
		// The next time round the two found are left out and the budget reaches the third.
		f = &fakeUsers{bad: map[int64]bool{50: true, 150: true, 250: true}}
		res, rejected, _, err = fetchUnskipped(seq(300), 100, 30, &skips, now.Add(time.Minute), f.fetch)
		if err != nil || !slices.Equal(rejected, []int64{250}) || len(res) != 297 {
			t.Fatalf("res=%d rejected=%v err=%v", len(res), rejected, err)
		}
	})
	t.Run("a flood wait stops the later batches", func(t *testing.T) {
		var skips badUserSkips
		f := &fakeUsers{floodAt: 2}
		res, _, wait, err := fetchUnskipped(seq(300), 100, 30, &skips, time.Unix(1_700_000_000, 0), f.fetch)
		if wait != 7*time.Second || err != nil || f.requests != 2 || !slices.Equal(res, seq(100)) {
			t.Fatalf("wait=%v err=%v requests=%d res=%d", wait, err, f.requests, len(res))
		}
	})
	t.Run("an error stops the later batches", func(t *testing.T) {
		var skips badUserSkips
		calls := 0
		_, rejected, _, err := fetchUnskipped(seq(300), 100, 30, &skips, time.Unix(1_700_000_000, 0), func([]int64) ([]int64, error) {
			calls++
			return nil, errors.New("connection reset")
		})
		if err == nil || calls != 1 || len(rejected) != 0 {
			t.Fatalf("err=%v calls=%d rejected=%v", err, calls, rejected)
		}
	})
}

func TestBadUserSkipsForgetsThoseNeverAskedAboutAgain(t *testing.T) {
	var skips badUserSkips
	now := time.Unix(1_700_000_000, 0)
	for _, id := range seq(50) {
		skips.add(id, now)
	}
	// Nobody asks about users 1 to 50 again (they left the groups); a later rejection clears them out.
	later := now.Add(presenceBadUserSkip)
	skips.add(51, later)
	if len(skips.until) != 1 || !skips.skipped(51, later) {
		t.Fatalf("skip list holds %d users, want only the one just added", len(skips.until))
	}
}
