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
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Telegram pushes updateUserStatus mostly to sessions that are online, so the
// bridge marks its session online while the Matrix user is active (like an
// open Telegram app would) and offline again after an idle timeout. While
// online, contacts' status changes arrive instantly instead of via polling.
const onlineRenewInterval = 60 * time.Second

type onlineWhileActive struct {
	lock      sync.Mutex
	online    bool
	lastSent  time.Time
	idleTimer *time.Timer
}

// markActive is called for every Matrix-side action of the user.
func (tc *TelegramClient) markActive(ctx context.Context) {
	cfg := tc.main.Config
	if !cfg.PresenceOnlineWhileActive || tc.metadata.IsBot || tc.client == nil {
		return
	}
	timeout := time.Duration(cfg.PresenceActiveTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	a := &tc.activity
	a.lock.Lock()
	defer a.lock.Unlock()
	if a.idleTimer != nil {
		a.idleTimer.Stop()
	}
	a.idleTimer = time.AfterFunc(timeout, func() { tc.setTelegramOnline(context.WithoutCancel(ctx), false) })
	if !a.online || time.Since(a.lastSent) >= onlineRenewInterval {
		a.online = true
		a.lastSent = time.Now()
		go tc.sendTelegramStatus(context.WithoutCancel(ctx), true)
	}
}

func (tc *TelegramClient) setTelegramOnline(ctx context.Context, online bool) {
	a := &tc.activity
	a.lock.Lock()
	if a.online == online {
		a.lock.Unlock()
		return
	}
	a.online = online
	a.lastSent = time.Now()
	a.lock.Unlock()
	tc.sendTelegramStatus(ctx, online)
}

func (tc *TelegramClient) sendTelegramStatus(ctx context.Context, online bool) {
	if _, err := tc.client.API().AccountUpdateStatus(ctx, !online); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Bool("online", online).Msg("Failed to update own Telegram online status")
	}
}
