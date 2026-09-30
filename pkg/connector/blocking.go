package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

var _ bridgev2.UserBlockingNetworkAPI = (*TelegramClient)(nil)

// ghostBlocker mirrors a block made on Telegram into Matrix; it is *bridgev2.UserLogin.
type ghostBlocker interface {
	SetGhostBlocked(ctx context.Context, ghostID networkid.UserID, blocked bool) error
}

// contactsBlocker is the part of the Telegram API that blocks and unblocks people; it is *tg.Client.
type contactsBlocker interface {
	ContactsBlock(ctx context.Context, request *tg.ContactsBlockRequest) (bool, error)
	ContactsUnblock(ctx context.Context, request *tg.ContactsUnblockRequest) (bool, error)
}

// blockedUserGhost is the ghost a block update is about. Only users can be blocked here; blocking a chat
// or channel isn't a thing on Telegram.
func blockedUserGhost(update *tg.UpdatePeerBlocked) (networkid.UserID, bool) {
	peer, ok := update.PeerID.(*tg.PeerUser)
	if !ok {
		return "", false
	}
	return ids.MakeUserID(peer.UserID), true
}

// applyPeerBlocked puts a block or unblock made on Telegram into the user's ignore list.
func applyPeerBlocked(ctx context.Context, blocker ghostBlocker, update *tg.UpdatePeerBlocked) error {
	ghostID, ok := blockedUserGhost(update)
	if !ok {
		zerolog.Ctx(ctx).Debug().Type("peer_type", update.PeerID).Msg("Ignoring block update for a non-user peer")
		return nil
	}
	return blocker.SetGhostBlocked(ctx, ghostID, update.Blocked)
}

func (tc *TelegramClient) onPeerBlocked(ctx context.Context, e tg.Entities, update *tg.UpdatePeerBlocked) error {
	return applyPeerBlocked(ctx, tc.userLogin, update)
}

// blockUser blocks or unblocks one user.
func blockUser(ctx context.Context, api contactsBlocker, peer tg.InputPeerClass, blocked bool) error {
	var ok bool
	var err error
	if blocked {
		ok, err = api.ContactsBlock(ctx, &tg.ContactsBlockRequest{ID: peer})
	} else {
		ok, err = api.ContactsUnblock(ctx, &tg.ContactsUnblockRequest{ID: peer})
	}
	if err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("telegram refused to change the block")
	}
	return nil
}

// HandleMatrixBlock blocks or unblocks a user on Telegram when their ghost is ignored or un-ignored in Matrix.
func (tc *TelegramClient) HandleMatrixBlock(ctx context.Context, ghost *bridgev2.Ghost, blocked bool) error {
	peerType, userID, err := ids.ParseUserID(ghost.ID)
	if err != nil {
		return err
	} else if peerType != ids.PeerTypeUser || userID == tc.telegramUserID {
		// Only other people can be blocked, not channels or yourself.
		return nil
	}
	accessHash, err := tc.ScopedStore.GetAccessHash(ctx, ids.PeerTypeUser, userID)
	if err != nil {
		return fmt.Errorf("failed to get access hash of %d: %w", userID, err)
	}
	return blockUser(ctx, tc.client.API(), &tg.InputPeerUser{UserID: userID, AccessHash: accessHash}, blocked)
}
