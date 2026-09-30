package connector

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

type fakeBlocker struct {
	calls []blockCall
	err   error
}

type blockCall struct {
	ghostID networkid.UserID
	blocked bool
}

func (f *fakeBlocker) SetGhostBlocked(_ context.Context, ghostID networkid.UserID, blocked bool) error {
	f.calls = append(f.calls, blockCall{ghostID, blocked})
	return f.err
}

func TestApplyPeerBlocked(t *testing.T) {
	tests := []struct {
		name   string
		update *tg.UpdatePeerBlocked
		want   []blockCall
	}{
		{"user blocked", &tg.UpdatePeerBlocked{Blocked: true, PeerID: &tg.PeerUser{UserID: 1234}}, []blockCall{{"1234", true}}},
		{"user unblocked", &tg.UpdatePeerBlocked{Blocked: false, PeerID: &tg.PeerUser{UserID: 1234}}, []blockCall{{"1234", false}}},
		{"channel ignored", &tg.UpdatePeerBlocked{Blocked: true, PeerID: &tg.PeerChannel{ChannelID: 55}}, nil},
		{"chat ignored", &tg.UpdatePeerBlocked{Blocked: true, PeerID: &tg.PeerChat{ChatID: 55}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocker := &fakeBlocker{}
			require.NoError(t, applyPeerBlocked(context.Background(), blocker, tt.update))
			assert.Equal(t, tt.want, blocker.calls)
		})
	}
	t.Run("error is returned", func(t *testing.T) {
		blocker := &fakeBlocker{err: errors.New("no double puppet")}
		err := applyPeerBlocked(context.Background(), blocker, &tg.UpdatePeerBlocked{Blocked: true, PeerID: &tg.PeerUser{UserID: 1}})
		assert.Error(t, err)
	})
}

type fakeAPI struct {
	blocked   []tg.InputPeerClass
	unblocked []tg.InputPeerClass
	ok        bool
	err       error
}

func (f *fakeAPI) ContactsBlock(_ context.Context, r *tg.ContactsBlockRequest) (bool, error) {
	f.blocked = append(f.blocked, r.ID)
	return f.ok, f.err
}

func (f *fakeAPI) ContactsUnblock(_ context.Context, r *tg.ContactsUnblockRequest) (bool, error) {
	f.unblocked = append(f.unblocked, r.ID)
	return f.ok, f.err
}

func TestBlockUser(t *testing.T) {
	peer := &tg.InputPeerUser{UserID: 1234, AccessHash: 99}
	t.Run("block", func(t *testing.T) {
		api := &fakeAPI{ok: true}
		require.NoError(t, blockUser(context.Background(), api, peer, true))
		assert.Equal(t, []tg.InputPeerClass{peer}, api.blocked)
		assert.Empty(t, api.unblocked)
	})
	t.Run("unblock", func(t *testing.T) {
		api := &fakeAPI{ok: true}
		require.NoError(t, blockUser(context.Background(), api, peer, false))
		assert.Equal(t, []tg.InputPeerClass{peer}, api.unblocked)
		assert.Empty(t, api.blocked)
	})
	t.Run("telegram says no", func(t *testing.T) {
		assert.Error(t, blockUser(context.Background(), &fakeAPI{ok: false}, peer, true))
	})
	t.Run("api error", func(t *testing.T) {
		assert.Error(t, blockUser(context.Background(), &fakeAPI{err: errors.New("flood")}, peer, false))
	})
}
