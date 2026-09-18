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
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tgerr"
)

// SyncedStickerPack records a Telegram sticker set that the automatic sticker pack sync
// imported into the user's space room, so later syncs can skip unchanged sets and remove
// uninstalled ones.
type SyncedStickerPack struct {
	// ShortName is the state key of the im.ponies.room_emotes event in the space room.
	ShortName string `json:"short_name"`
	// Hash is the Telegram StickerSet.hash at the time of import.
	Hash int `json:"hash"`
}

const (
	// stickerSyncInitialDelay leaves the connection time to settle (chat sync, updates
	// catch-up) before the potentially long pack import starts.
	stickerSyncInitialDelay = 30 * time.Second
	// stickerSyncDebounce coalesces bursts of sticker set updates into one sync.
	stickerSyncDebounce = 5 * time.Second
	// stickerSyncSetInterval rate-limits imports: one set per interval.
	stickerSyncSetInterval = 2 * time.Second
)

// stickerSetSyncDiff decides which installed sets need a (re)import and which previously
// synced sets were uninstalled. installed must contain every installed set (stickers and
// custom emojis); synced is keyed by the decimal set ID.
func stickerSetSyncDiff(installed []tg.StickerSet, synced map[string]SyncedStickerPack) (toImport []tg.StickerSet, toRemove []string) {
	seen := make(map[string]struct{}, len(installed))
	for _, set := range installed {
		if set.ShortName == "" {
			continue
		}
		key := strconv.FormatInt(set.ID, 10)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		prev, ok := synced[key]
		if !ok || prev.Hash != set.Hash || prev.ShortName != set.ShortName {
			toImport = append(toImport, set)
		}
	}
	for key := range synced {
		if _, ok := seen[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	slices.Sort(toRemove)
	return
}

// triggerStickerPackSync asks the sync loop to run soon. It never blocks.
func (tc *TelegramClient) triggerStickerPackSync() {
	if tc.stickerSyncTrigger == nil {
		return
	}
	select {
	case tc.stickerSyncTrigger <- struct{}{}:
	default:
	}
}

// runStickerPackSyncLoop runs one sync after connecting and one after every burst of
// sticker set updates, until ctx is cancelled (i.e. the client disconnects).
func (tc *TelegramClient) runStickerPackSyncLoop(ctx context.Context) {
	if !tc.main.Config.StickerPackSync || tc.metadata.IsBot {
		return
	}
	log := zerolog.Ctx(ctx).With().Str("action", "sticker_pack_sync").Logger()
	ctx = log.WithContext(ctx)
	timer := time.NewTimer(stickerSyncInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tc.stickerSyncTrigger:
			timer.Reset(stickerSyncDebounce)
			continue
		case <-timer.C:
		}
		if err := tc.syncStickerPacks(ctx); err != nil && ctx.Err() == nil {
			log.Err(err).Msg("Sticker pack sync failed")
		}
	}
}

func (tc *TelegramClient) fetchInstalledStickerSets(ctx context.Context) ([]tg.StickerSet, error) {
	resp, err := tc.client.API().MessagesGetAllStickers(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get installed sticker sets: %w", err)
	}
	stickers, ok := resp.(*tg.MessagesAllStickers)
	if !ok {
		return nil, fmt.Errorf("unexpected getAllStickers response %T", resp)
	}
	emojiResp, err := tc.client.API().MessagesGetEmojiStickers(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get installed emoji sets: %w", err)
	}
	emojis, ok := emojiResp.(*tg.MessagesAllStickers)
	if !ok {
		return nil, fmt.Errorf("unexpected getEmojiStickers response %T", emojiResp)
	}
	return append(slices.Clone(stickers.Sets), emojis.Sets...), nil
}

func (tc *TelegramClient) syncStickerPacks(ctx context.Context) error {
	tc.stickerSyncLock.Lock()
	defer tc.stickerSyncLock.Unlock()
	log := zerolog.Ctx(ctx)

	spaceRoom, err := tc.userLogin.GetSpaceRoom(ctx)
	if err != nil {
		return fmt.Errorf("failed to get space room: %w", err)
	} else if spaceRoom == "" {
		log.Debug().Msg("Personal filtering spaces are disabled, not syncing sticker packs")
		return nil
	}
	installed, err := tc.fetchInstalledStickerSets(ctx)
	if err != nil {
		return err
	}
	synced := tc.metadata.StickerPacks
	toImport, toRemove := stickerSetSyncDiff(installed, synced)
	if len(toImport) == 0 && len(toRemove) == 0 {
		log.Debug().Int("installed", len(installed)).Msg("Sticker packs are up to date")
		return nil
	}
	log.Info().
		Int("installed", len(installed)).
		Int("to_import", len(toImport)).
		Int("to_remove", len(toRemove)).
		Msg("Syncing sticker packs to space room")

	// Copy-on-write: the metadata map may be marshaled concurrently by other saves.
	next := maps.Clone(synced)
	if next == nil {
		next = make(map[string]SyncedStickerPack)
	}
	commit := func() {
		tc.metadata.StickerPacks = maps.Clone(next)
		if err := tc.userLogin.Save(ctx); err != nil {
			log.Err(err).Msg("Failed to save sticker pack sync state")
		}
	}

	for _, key := range toRemove {
		prev := next[key]
		if prev.ShortName != "" {
			_, err = tc.main.Bridge.Bot.SendState(ctx, spaceRoom, event.StateImagePack, prev.ShortName, &event.Content{}, time.Now())
			if err != nil {
				log.Err(err).Str("short_name", prev.ShortName).Msg("Failed to remove uninstalled sticker pack")
				continue
			}
		}
		log.Debug().Str("short_name", prev.ShortName).Msg("Removed uninstalled sticker pack")
		delete(next, key)
	}
	if len(toRemove) > 0 {
		commit()
	}

	for i, set := range toImport {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(stickerSyncSetInterval):
			}
		}
		if err = tc.importStickerSetToSpace(ctx, spaceRoom, set); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Not recorded, so the next sync retries it. Transferred media is cached by
			// document ID, so a retry doesn't re-upload stickers that already made it.
			log.Err(err).Str("short_name", set.ShortName).Msg("Failed to import sticker pack")
			continue
		}
		key := strconv.FormatInt(set.ID, 10)
		if prev, ok := next[key]; ok && prev.ShortName != "" && prev.ShortName != set.ShortName {
			// The set's short name changed, so the old state key is now orphaned.
			_, err = tc.main.Bridge.Bot.SendState(ctx, spaceRoom, event.StateImagePack, prev.ShortName, &event.Content{}, time.Now())
			if err != nil {
				log.Warn().Err(err).Str("short_name", prev.ShortName).Msg("Failed to remove renamed sticker pack")
			}
		}
		next[key] = SyncedStickerPack{ShortName: set.ShortName, Hash: set.Hash}
		commit()
	}
	log.Info().Msg("Sticker pack sync finished")
	return nil
}

func (tc *TelegramClient) importStickerSetToSpace(ctx context.Context, spaceRoom id.RoomID, set tg.StickerSet) error {
	var pack, err = tc.DownloadImagePack(ctx, set.ShortName)
	for attempt := 0; err != nil && attempt < 3; attempt++ {
		wait, ok := tgerr.AsFloodWait(err)
		if !ok {
			break
		}
		zerolog.Ctx(ctx).Warn().Dur("wait", wait).Str("short_name", set.ShortName).Msg("Flood wait while importing sticker pack")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait + time.Second):
		}
		pack, err = tc.DownloadImagePack(ctx, set.ShortName)
	}
	if err != nil {
		return err
	}
	return tc.sendImagePackToSpace(ctx, spaceRoom, pack.Shortcode, pack.Content, pack.Extra)
}

func (tc *TelegramClient) sendImagePackToSpace(ctx context.Context, spaceRoom id.RoomID, stateKey string, content *event.ImagePackEventContent, extra map[string]any) error {
	if stateKey == "" && content.Metadata.BridgedPack != nil {
		stateKey = content.Metadata.BridgedPack.URL
	}
	_, err := tc.main.Bridge.Bot.SendState(ctx, spaceRoom, event.StateImagePack, stateKey, &event.Content{
		Parsed: content,
		Raw:    extra,
	}, time.Now())
	return err
}
