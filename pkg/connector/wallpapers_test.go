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
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestWallpaperMediaIDRoundTrip(t *testing.T) {
	info := ids.DirectMediaInfo{
		PeerType:  ids.FakePeerTypeWallpaper,
		PeerID:    5944837766217924609,
		UserID:    12345,
		MessageID: -7283727438127212,
		ID:        5944837766217924610,
		Thumbnail: true,
	}
	mediaID, err := info.AsMediaID()
	require.NoError(t, err)
	parsed, err := ids.ParseDirectMediaInfo(mediaID)
	require.NoError(t, err)
	assert.Equal(t, info, parsed)
}

// A colour Telegram leaves out and a black one differ: the client drops a missing stop but keeps black.
func TestWallpaperSettingsKeepAbsentColoursAbsent(t *testing.T) {
	var settings tg.WallPaperSettings
	settings.SetBackgroundColor(0)
	settings.SetSecondBackgroundColor(0x6ba587)
	settings.SetIntensity(-50)
	converted := convertWallpaperSettings(settings)
	require.NotNil(t, converted.BackgroundColor)
	assert.Equal(t, 0, *converted.BackgroundColor)
	assert.Equal(t, 0x6ba587, *converted.SecondBackgroundColor)
	assert.Nil(t, converted.ThirdBackgroundColor)
	assert.Nil(t, converted.FourthBackgroundColor)
	assert.Equal(t, -50, *converted.Intensity)
}

func TestColourOnlyWallpaper(t *testing.T) {
	paper := &tg.WallPaperNoFile{ID: 42, Dark: true}
	var settings tg.WallPaperSettings
	settings.SetBackgroundColor(0x1e3557)
	paper.SetSettings(settings)
	wallpaper, err := (&TelegramClient{}).convertWallpaper(context.Background(), paper)
	require.NoError(t, err)
	assert.Equal(t, "42", wallpaper.ID)
	assert.Equal(t, "fill", wallpaper.Kind)
	assert.True(t, wallpaper.Dark)
	assert.Empty(t, wallpaper.URL)
	assert.Equal(t, 0x1e3557, *wallpaper.Settings.BackgroundColor)
}

func TestGunzipPattern(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	var packed bytes.Buffer
	writer := gzip.NewWriter(&packed)
	_, err := writer.Write(svg)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	out, err := gunzipPattern(packed.Bytes())
	require.NoError(t, err)
	assert.Equal(t, svg, out)

	// Already plain: passed on as it is.
	out, err = gunzipPattern(svg)
	require.NoError(t, err)
	assert.Equal(t, svg, out)
}
