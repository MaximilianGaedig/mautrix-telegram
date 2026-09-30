package connector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

const testArchiveTag = event.RoomTagLowPriority

func TestDialogRoomTag(t *testing.T) {
	tests := []struct {
		name       string
		archiveTag event.RoomTag
		folderID   int
		pinned     bool
		want       event.RoomTag
	}{
		{"main list", testArchiveTag, 0, false, ""},
		{"pinned", testArchiveTag, 0, true, event.RoomTagFavourite},
		{"archived", testArchiveTag, 1, false, testArchiveTag},
		{"archived beats pin", testArchiveTag, 1, true, testArchiveTag},
		{"archive off, archived", "", 1, false, ""},
		{"archive off, archived and pinned", "", 1, true, event.RoomTagFavourite},
		{"custom tag", "u.archive", 1, false, "u.archive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dialogRoomTag(tt.archiveTag, tt.folderID, tt.pinned))
		})
	}
}

func TestFillUserLocalMetaArchive(t *testing.T) {
	tc := &TelegramClient{main: &TelegramConnector{Config: TelegramConfig{ArchiveTag: testArchiveTag}}}
	archived := &tg.Dialog{}
	archived.SetFolderID(1)
	pinned := &tg.Dialog{}
	pinned.SetPinned(true)
	tests := []struct {
		name   string
		dialog *tg.Dialog
		want   *event.RoomTag
	}{
		{"archived dialog", archived, ptrTag(testArchiveTag)},
		{"pinned dialog", pinned, ptrTag(event.RoomTagFavourite)},
		{"plain dialog", &tg.Dialog{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &bridgev2.ChatInfo{}
			tc.fillUserLocalMeta(info, tt.dialog)
			require.NotNil(t, info.UserLocal)
			assert.Equal(t, tt.want, info.UserLocal.Tag)
		})
	}
	// With the option off an archived chat gets no tag
	tc.main.Config.ArchiveTag = ""
	info := &bridgev2.ChatInfo{}
	tc.fillUserLocalMeta(info, archived)
	assert.Nil(t, info.UserLocal.Tag)
}

func ptrTag(tag event.RoomTag) *event.RoomTag { return &tag }

func tagMsg(prev, now []event.RoomTag) *bridgev2.MatrixRoomTag {
	toTags := func(list []event.RoomTag) *event.TagEventContent {
		content := &event.TagEventContent{Tags: event.Tags{}}
		for _, tag := range list {
			content.Tags[tag] = event.TagMetadata{}
		}
		return content
	}
	msg := &bridgev2.MatrixRoomTag{PrevContent: toTags(prev)}
	msg.Content = toTags(now)
	return msg
}

func boolPtr(b bool) *bool { return &b }

func TestRoomTagActions(t *testing.T) {
	fav, arch := event.RoomTagFavourite, testArchiveTag
	other := event.RoomTag("u.work")
	tests := []struct {
		name       string
		archiveTag event.RoomTag
		prev, now  []event.RoomTag
		want       roomTagActions
	}{
		{"pin", arch, nil, []event.RoomTag{fav}, roomTagActions{Pin: boolPtr(true)}},
		{"unpin", arch, []event.RoomTag{fav}, nil, roomTagActions{Pin: boolPtr(false)}},
		{"archive", arch, nil, []event.RoomTag{arch}, roomTagActions{Archive: boolPtr(true)}},
		{"unarchive", arch, []event.RoomTag{arch}, nil, roomTagActions{Archive: boolPtr(false)}},
		{"unrelated tag while pinned does not unpin", arch, []event.RoomTag{fav}, []event.RoomTag{fav, other}, roomTagActions{}},
		{"unrelated tag while archived does not unarchive", arch, []event.RoomTag{arch}, []event.RoomTag{arch, other}, roomTagActions{}},
		{"unrelated tag alone", arch, nil, []event.RoomTag{other}, roomTagActions{}},
		{"pin an archived chat only pins", arch, []event.RoomTag{arch}, []event.RoomTag{arch, fav}, roomTagActions{Pin: boolPtr(true)}},
		{"move from archive to favourite", arch, []event.RoomTag{arch}, []event.RoomTag{fav}, roomTagActions{Pin: boolPtr(true), Archive: boolPtr(false)}},
		{"archive off ignores the tag", "", nil, []event.RoomTag{arch}, roomTagActions{}},
		{"archive tag misconfigured as favourite never archives", fav, nil, []event.RoomTag{fav}, roomTagActions{Pin: boolPtr(true)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getRoomTagActions(tagMsg(tt.prev, tt.now), tt.archiveTag))
		})
	}
}

func TestValidateArchiveTag(t *testing.T) {
	tc := &TelegramConnector{Config: TelegramConfig{APIID: 1, APIHash: "abc"}}
	tc.Config.AnimatedSticker.Target = "disable"
	assert.NoError(t, tc.ValidateConfig())
	tc.Config.ArchiveTag = event.RoomTagLowPriority
	assert.NoError(t, tc.ValidateConfig())
	tc.Config.ArchiveTag = event.RoomTagFavourite
	assert.Error(t, tc.ValidateConfig())
}
