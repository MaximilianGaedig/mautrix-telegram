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
	"context"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// StateBotCommands lists a Telegram bot's commands in its DM portal, so clients can offer
// "/command" completion like Telegram does.
var StateBotCommands = event.Type{Type: "fi.mau.telegram.bot_commands", Class: event.StateEventType}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type BotCommandsContent struct {
	Bot      string       `json:"bot"`
	Commands []BotCommand `json:"commands"`
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

// botCommandsUpdater publishes the bot's command list in its DM portal. It never changes
// portal metadata, so it always returns false.
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
		content := &BotCommandsContent{Bot: ghost.Intent.GetMXID().String(), Commands: botCommandsFromInfo(botInfo)}
		_, err = tc.main.Bridge.Bot.SendState(ctx, portal.MXID, StateBotCommands, "", &event.Content{Parsed: content}, time.Now())
		if err != nil {
			log.Warn().Err(err).Msg("Failed to send bot command list")
		}
		return false
	}
}
