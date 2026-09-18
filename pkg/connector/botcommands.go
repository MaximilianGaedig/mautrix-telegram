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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// StateBotCommands lists the commands of the Telegram bots in a portal, so clients can offer
// "/command" completion like Telegram does. See docs/bot-commands.md for the format.
var StateBotCommands = event.Type{Type: "fi.mau.telegram.bot_commands", Class: event.StateEventType}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// BotCommandsEntry is one bot's command list.
type BotCommandsEntry struct {
	Bot string `json:"bot"`
	// Username is the bot's Telegram username without @. In groups, commands are
	// addressed as /command@username.
	Username string       `json:"username,omitempty"`
	Commands []BotCommand `json:"commands"`
}

// BotCommandsContent is the content of StateBotCommands. DM portals also set the legacy
// top-level Bot/Commands fields (the single bot in the chat); groups only set Bots.
type BotCommandsContent struct {
	Bot      string             `json:"bot,omitempty"`
	Commands []BotCommand       `json:"commands,omitzero"`
	Bots     []BotCommandsEntry `json:"bots"`
}

func botCommandsFromInfo(info tg.BotInfo) []BotCommand {
	out := make([]BotCommand, 0, len(info.Commands))
	for _, c := range info.Commands {
		if c.Command != "" {
			out = append(out, BotCommand{Command: c.Command, Description: c.Description})
		}
	}
	return out
}

// telegramUsername returns the user's primary username, or the first active collectible one.
func telegramUsername(user *tg.User) string {
	if user == nil {
		return ""
	}
	if user.Username != "" {
		return user.Username
	}
	for _, u := range user.Usernames {
		if u.Active {
			return u.Username
		}
	}
	return ""
}

// collectBotCommands turns the bot_info list of a full chat into per-bot entries, skipping
// bots without commands. Entries are sorted by username (then user ID) so the content is
// stable across syncs.
func collectBotCommands(infos []tg.BotInfo, users []tg.UserClass, mxidFor func(userID int64) id.UserID) []BotCommandsEntry {
	usernames := make(map[int64]string, len(users))
	for _, rawUser := range users {
		if user, ok := rawUser.(*tg.User); ok {
			usernames[user.ID] = telegramUsername(user)
		}
	}
	seen := make(map[int64]struct{}, len(infos))
	out := make([]BotCommandsEntry, 0, len(infos))
	for _, info := range infos {
		if info.UserID == 0 {
			continue
		}
		if _, dup := seen[info.UserID]; dup {
			continue
		}
		cmds := botCommandsFromInfo(info)
		if len(cmds) == 0 {
			continue
		}
		seen[info.UserID] = struct{}{}
		out = append(out, BotCommandsEntry{
			Bot:      mxidFor(info.UserID).String(),
			Username: usernames[info.UserID],
			Commands: cmds,
		})
	}
	slices.SortFunc(out, func(a, b BotCommandsEntry) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username)), cmp.Compare(a.Bot, b.Bot))
	})
	return out
}

// botCommandsStateHash identifies the last sent content (and room) so resyncs don't send
// identical state events.
func botCommandsStateHash(roomID id.RoomID, content *BotCommandsContent) string {
	data, _ := json.Marshal(content)
	sum := sha256.Sum256(append([]byte(roomID+"\n"), data...))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// sendBotCommandsState sends the content if it differs from what was last sent to the
// portal room. It returns true if the portal metadata changed.
func (tc *TelegramClient) sendBotCommandsState(ctx context.Context, portal *bridgev2.Portal, content *BotCommandsContent) bool {
	meta := portal.Metadata.(*PortalMetadata)
	if len(content.Bots) == 0 && meta.BotCommandsHash == "" {
		return false
	}
	hash := botCommandsStateHash(portal.MXID, content)
	if hash == meta.BotCommandsHash {
		return false
	}
	_, err := tc.main.Bridge.Bot.SendState(ctx, portal.MXID, StateBotCommands, "", &event.Content{Parsed: content}, time.Now())
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to send bot command list")
		return false
	}
	meta.BotCommandsHash = hash
	return true
}

// botCommandsUpdater publishes the bot's command list in its DM portal.
func (tc *TelegramClient) botCommandsUpdater(userID int64) bridgev2.ExtraUpdater[*bridgev2.Portal] {
	return func(ctx context.Context, portal *bridgev2.Portal) bool {
		if portal.MXID == "" || tc.metadata.IsBot {
			return false
		}
		ghost, err := tc.main.Bridge.GetExistingGhostByID(ctx, ids.MakeUserID(userID))
		if err != nil || ghost == nil || !ghost.IsBot {
			return false
		}
		log := zerolog.Ctx(ctx).With().Int64("bot_id", userID).Logger()
		accessHash, err := tc.ScopedStore.GetAccessHash(ctx, ids.PeerTypeUser, userID)
		if err != nil || accessHash == 0 {
			return false
		}
		full, err := tc.client.API().UsersGetFullUser(ctx, &tg.InputUser{UserID: userID, AccessHash: accessHash})
		if err != nil {
			log.Debug().Err(err).Msg("Failed to get bot info for command list")
			return false
		}
		botInfo, ok := full.FullUser.GetBotInfo()
		if !ok {
			return false
		}
		if botInfo.UserID == 0 {
			botInfo.UserID = userID
		}
		content := &BotCommandsContent{
			Bot:      ghost.Intent.GetMXID().String(),
			Commands: botCommandsFromInfo(botInfo),
			Bots: collectBotCommands([]tg.BotInfo{botInfo}, full.Users, func(int64) id.UserID {
				return ghost.Intent.GetMXID()
			}),
		}
		return tc.sendBotCommandsState(ctx, portal, content)
	}
}

// groupBotCommandsUpdater publishes the command lists of the bots in a group or channel,
// taken from the bot_info of the full chat.
func (tc *TelegramClient) groupBotCommandsUpdater(infos []tg.BotInfo, users []tg.UserClass) bridgev2.ExtraUpdater[*bridgev2.Portal] {
	entries := collectBotCommands(infos, users, func(userID int64) id.UserID {
		return tc.main.Bridge.Matrix.FormatGhostMXID(ids.MakeUserID(userID))
	})
	return func(ctx context.Context, portal *bridgev2.Portal) bool {
		if portal.MXID == "" || tc.metadata.IsBot {
			return false
		}
		return tc.sendBotCommandsState(ctx, portal, &BotCommandsContent{Bots: entries})
	}
}
