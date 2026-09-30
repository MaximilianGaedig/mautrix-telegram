package connector

import (
	"context"
	"fmt"
	"slices"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// archiveFolderID is the Telegram folder of archived chats. Folder 0 is the main chat list.
const archiveFolderID = 1

// dialogRoomTag is the room tag a chat has for its Telegram state: the archive tag for an archived chat
// (when archive_tag is set), m.favourite for a pinned one, and none otherwise.
func dialogRoomTag(archiveTag event.RoomTag, folderID int, pinned bool) event.RoomTag {
	switch {
	case archiveTag != "" && folderID == archiveFolderID:
		return archiveTag
	case pinned:
		return event.RoomTagFavourite
	}
	return ""
}

// roomTagActions is what a change to a room's tags means on Telegram: whether to pin (true) or unpin (false)
// the chat, and whether to archive (true) or unarchive (false) it. A nil means that tag didn't change, so
// nothing is done for it and an unrelated tag never unpins or unarchives a chat.
type roomTagActions struct {
	Pin     *bool
	Archive *bool
}

func getRoomTagActions(msg *bridgev2.MatrixRoomTag, archiveTag event.RoomTag) roomTagActions {
	actions := roomTagActions{Pin: bridgev2.TagChange(msg, event.RoomTagFavourite)}
	if archiveTag != event.RoomTagFavourite {
		actions.Archive = bridgev2.TagChange(msg, archiveTag)
	}
	return actions
}

func (tc *TelegramClient) setArchived(ctx context.Context, peer tg.InputPeerClass, archived bool) error {
	folderID := 0
	if archived {
		folderID = archiveFolderID
	}
	_, err := tc.client.API().FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{Peer: peer, FolderID: folderID}})
	return err
}

// onFolderPeers applies chats moving into or out of the archive folder to their rooms' tags.
func (tc *TelegramClient) onFolderPeers(ctx context.Context, update *tg.UpdateFolderPeers) error {
	archiveTag := tc.main.Config.ArchiveTag
	if archiveTag == "" {
		return nil
	}
	for _, folderPeer := range update.FolderPeers {
		portalKey := tc.makePortalKeyFromPeer(folderPeer.Peer, 0)
		tag := dialogRoomTag(archiveTag, folderPeer.FolderID, slices.Contains(tc.metadata.PinnedDialogs, portalKey.ID))
		folderID := folderPeer.FolderID
		res := tc.main.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.ChatInfoChange{
			ChatInfoChange: &bridgev2.ChatInfoChange{
				ChatInfo: &bridgev2.ChatInfo{
					UserLocal: &bridgev2.UserLocalPortalInfo{Tag: ptr.Ptr(tag)},
				},
			},
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventChatInfoChange,
				PortalKey: portalKey,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Str("tg_event", "updateFolderPeers").Int("folder_id", folderID).Str("tag", string(tag))
				},
			},
		})
		if err := resultToError(res); err != nil {
			return fmt.Errorf("failed to update tag of %s: %w", portalKey.ID, err)
		}
	}
	return nil
}
