// mautrix-telegram - A Matrix-Telegram puppeting bridge.
// Copyright (C) 2025 Sumner Evans
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
	"context"
	"sync"

	"go.mau.fi/util/exsync"
	"go.mau.fi/util/jsontime"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-telegram/pkg/gotd/crypto"
	"go.mau.fi/mautrix-telegram/pkg/gotd/session"
)

func (tc *TelegramConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Ghost:     func() any { return &GhostMetadata{} },
		Portal:    func() any { return &PortalMetadata{} },
		Message:   func() any { return &MessageMetadata{} },
		Reaction:  nil,
		UserLogin: func() any { return &UserLoginMetadata{} },
	}
}

type GhostMetadata struct {
	IsPremium bool `json:"is_premium,omitempty"`
	Deleted   bool `json:"deleted,omitempty"`
	NotMin    bool `json:"not_min,omitempty"`

	ContactSource   int64 `json:"contact_source,omitempty"`
	SourceIsContact bool  `json:"source_is_contact,omitempty"`

	// BioFetched is when the bio was last requested, so it is fetched at most once a day.
	BioFetched jsontime.Unix `json:"bio_fetched,omitempty"`
}

func (gm *GhostMetadata) IsMin() bool {
	return !gm.NotMin
}

type PortalMetadata struct {
	IsSuperGroup      bool          `json:"is_supergroup,omitempty"`
	IsForumGeneral    bool          `json:"is_forum_general,omitempty"`
	IsCommunity       bool          `json:"is_community,omitempty"`
	ReadUpTo          int           `json:"read_up_to,omitempty"` // FIXME this shouldn't be here
	AllowedReactions  []string      `json:"allowed_reactions"`
	LastSync          jsontime.Unix `json:"last_sync,omitempty"`
	FullSynced        bool          `json:"full_synced,omitempty"`
	ParticipantsCount int           `json:"member_count,omitempty"`
	// BotCommandsHash identifies the last fi.mau.telegram.bot_commands state sent.
	BotCommandsHash string `json:"bot_commands_hash,omitempty"`

	SponsoredMessagePollTS    jsontime.Unix       `json:"sponsored_message_poll_ts,omitempty"`
	SponsoredMessageEventID   id.EventID          `json:"sponsored_message_event_id,omitempty"`
	SponsoredMessageRandomID  []byte              `json:"sponsored_message_random_id,omitempty"`
	LastMessageOnSponsorFetch networkid.MessageID `json:"last_message_on_sponsor_fetch,omitempty"`

	sponsoredMessageLock sync.Mutex
	sponsoredMessageSeen *exsync.Set[int64]
}

func (pm *PortalMetadata) SetIsSuperGroup(isSupergroup bool) (changed bool) {
	changed = pm.IsSuperGroup != isSupergroup
	pm.IsSuperGroup = isSupergroup
	return changed
}

func (pm *PortalMetadata) SetIsCommunity(isCommunity bool) (changed bool) {
	changed = pm.IsCommunity != isCommunity
	pm.IsCommunity = isCommunity
	return changed
}

func (pm *PortalMetadata) SetIsForumGeneral(isForumGeneral bool) (changed bool) {
	changed = pm.IsForumGeneral != isForumGeneral
	pm.IsForumGeneral = isForumGeneral
	return changed
}

type MessageMetadata struct {
	ContentHash []byte              `json:"content_hash,omitempty"`
	ContentURI  id.ContentURIString `json:"content_uri,omitempty"`
	// GroupedID is the Telegram album (grouped_id) the message belongs to.
	// It's used to compute the fi.mau.album index of later album items.
	GroupedID int64 `json:"grouped_id,omitempty"`
	// Poll is set on the message that carries a poll, and only there.
	Poll *PollMetadata `json:"poll,omitempty"`
	// LiveLocation marks a live location bridged as a Matrix one: the part is its beacon_info, and edits
	// of it are new positions.
	LiveLocation bool `json:"live_location,omitempty"`
}

// PollMetadata is what the bridge needs to remember about a poll to bridge its votes.
type PollMetadata struct {
	// MatrixOptions maps the answer IDs of a poll that was started on Matrix to the option bytes Telegram was
	// given for them. Polls that started on Telegram use the hex of the option bytes as the answer ID instead.
	MatrixOptions map[string][]byte `json:"matrix_options,omitempty"`
	// MaxSelections is how many answers a single vote may pick.
	MaxSelections int  `json:"max_selections,omitempty"`
	PublicVoters  bool `json:"public_voters,omitempty"`
	// Closed is set once the poll end has been bridged to Matrix.
	Closed bool `json:"closed,omitempty"`
	// OwnVote is the options key (see pollOptionsKey) of the last vote of the logged-in user that has been
	// bridged, in either direction. It's empty when the user hasn't voted.
	OwnVote string `json:"own_vote,omitempty"`
}

type UserLoginMetadata struct {
	LoginPhone  string           `json:"phone,omitempty"`
	LoginMethod string           `json:"login_method,omitempty"`
	IsBot       bool             `json:"is_bot,omitempty"`
	Session     UserLoginSession `json:"session"`
	TakeoutID   int64            `json:"takeout_id,omitempty"`

	DialogSyncComplete bool               `json:"takeout_portal_crawl_done,omitempty"`
	DialogSyncCursor   networkid.PortalID `json:"takeout_portal_crawl_cursor,omitempty"`
	DialogSyncCount    int                `json:"dialog_sync_count,omitempty"`

	PinnedDialogs []networkid.PortalID `json:"pinned_dialogs,omitempty"`

	PushEncryptionKey []byte `json:"push_encryption_key,omitempty"`

	// StickerPacks tracks sets imported by the automatic sticker pack sync, keyed by
	// decimal set ID. Treat as immutable: replace the map instead of mutating it.
	StickerPacks map[string]SyncedStickerPack `json:"sticker_packs,omitempty"`
}

func (u *UserLoginMetadata) ResetOnLogout() {
	u.Session.AuthKey = nil
	u.TakeoutID = 0
	u.DialogSyncComplete = false
	u.DialogSyncCursor = networkid.PortalID("")
	u.DialogSyncCount = 0
	u.PushEncryptionKey = nil
}

type UserLoginSession struct {
	AuthKey       []byte `json:"auth_key,omitempty"`
	Datacenter    int    `json:"dc_id,omitempty"`
	ServerAddress string `json:"server_address,omitempty"`
	Salt          int64  `json:"salt,omitempty"`
}

func (u UserLoginSession) HasAuthKey() bool {
	return len(u.AuthKey) == 256
}

func (s *UserLoginSession) Load(_ context.Context) (*session.Data, error) {
	if !s.HasAuthKey() {
		return nil, session.ErrNotFound
	}
	keyID := crypto.Key(s.AuthKey).ID()
	return &session.Data{
		DC:        s.Datacenter,
		Addr:      s.ServerAddress,
		AuthKey:   s.AuthKey,
		AuthKeyID: keyID[:],
		Salt:      s.Salt,
	}, nil
}

func (s *UserLoginSession) Save(ctx context.Context, data *session.Data) error {
	s.Datacenter = data.DC
	s.ServerAddress = data.Addr
	s.AuthKey = data.AuthKey
	s.Salt = data.Salt
	// TODO save UserLogin to database?
	return nil
}

func updatePortalLastSyncAt(_ context.Context, portal *bridgev2.Portal) bool {
	meta := portal.Metadata.(*PortalMetadata)
	meta.LastSync = jsontime.UnixNow()
	return true
}
