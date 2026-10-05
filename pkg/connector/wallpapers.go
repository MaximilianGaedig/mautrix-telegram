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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/mediaproxy"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/connector/media"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// Telegram's chat wallpapers for Matrix clients: the list of the account's wallpapers over the provisioning API, and
// their files as direct media, so a client downloads only the pattern or picture of the wallpaper it shows.

// patternMIMEType is a wallpaper pattern as Telegram stores it: a gzipped SVG.
const patternMIMEType = "application/x-tgwallpattern"

// WallpaperSettings mirrors Telegram's wallPaperSettings, absent fields left out: a client reads the colours with
// the same rules Telegram's own apps use, in which a missing colour and a black one differ.
type WallpaperSettings struct {
	BackgroundColor       *int `json:"background_color,omitempty"`
	SecondBackgroundColor *int `json:"second_background_color,omitempty"`
	ThirdBackgroundColor  *int `json:"third_background_color,omitempty"`
	FourthBackgroundColor *int `json:"fourth_background_color,omitempty"`
	// Intensity is how strongly a pattern shows, -100..100; negative for a dark wallpaper's pattern.
	Intensity *int `json:"intensity,omitempty"`
	Rotation  *int `json:"rotation,omitempty"`
	Blur      bool `json:"blur,omitempty"`
	Motion    bool `json:"motion,omitempty"`
}

// Wallpaper is one of the account's wallpapers.
type Wallpaper struct {
	// ID is the wallpaper's Telegram ID, as a string because it does not fit a JavaScript number.
	ID      string `json:"id"`
	Slug    string `json:"slug,omitempty"`
	Default bool   `json:"default,omitempty"`
	Dark    bool   `json:"dark,omitempty"`
	// Kind is "pattern" (an SVG drawn over the colours), "image" (a picture) or "fill" (the colours alone).
	Kind     string             `json:"kind"`
	Settings *WallpaperSettings `json:"settings,omitempty"`
	// URL is the pattern (as SVG) or the picture, downloaded from Telegram when it is first asked for.
	URL id.ContentURIString `json:"url,omitempty"`
	// ThumbnailURL is a small preview of the pattern or picture, for a picker.
	ThumbnailURL id.ContentURIString `json:"thumbnail_url,omitempty"`
}

func (tc *TelegramConnector) setUpWallpaperAPI() error {
	c, ok := tc.Bridge.Matrix.(bridgev2.MatrixConnectorWithProvisioning)
	if !ok {
		return errors.New("matrix connector has no provisioning API")
	}
	prov := c.GetProvisioning()
	router := prov.GetRouter()
	if router == nil {
		return errors.New("provisioning API has no router")
	}
	router.HandleFunc("GET /v3/wallpapers", func(w http.ResponseWriter, r *http.Request) {
		tc.getWallpapers(w, r, prov.GetUser(r))
	})
	return nil
}

func (tc *TelegramConnector) getWallpapers(w http.ResponseWriter, r *http.Request, user *bridgev2.User) {
	var client *TelegramClient
	for _, login := range user.GetUserLogins() {
		if c, ok := login.Client.(*TelegramClient); ok && c.IsLoggedIn() {
			client = c
			break
		}
	}
	if client == nil {
		mautrix.MForbidden.WithMessage("Not logged in to Telegram").Write(w)
		return
	}
	if !tc.useDirectMedia {
		mautrix.MUnrecognized.WithMessage("Direct media is off, so wallpaper files cannot be served").Write(w)
		return
	}
	wallpapers, err := client.listWallpapers(r.Context())
	if err != nil {
		zerolog.Ctx(r.Context()).Err(err).Msg("Failed to list wallpapers")
		mautrix.MUnknown.WithMessage("Failed to get wallpapers from Telegram").Write(w)
		return
	}
	exhttp.WriteJSONResponse(w, http.StatusOK, map[string]any{"wallpapers": wallpapers})
}

func (t *TelegramClient) listWallpapers(ctx context.Context) ([]Wallpaper, error) {
	resp, err := t.client.API().AccountGetWallPapers(ctx, 0)
	if err != nil {
		return nil, err
	}
	papers, ok := resp.(*tg.AccountWallPapers)
	if !ok {
		return nil, fmt.Errorf("unexpected response %T", resp)
	}
	out := make([]Wallpaper, 0, len(papers.Wallpapers))
	for _, raw := range papers.Wallpapers {
		wallpaper, err := t.convertWallpaper(ctx, raw)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Skipping wallpaper")
			continue
		}
		out = append(out, wallpaper)
	}
	return out, nil
}

func convertWallpaperSettings(settings tg.WallPaperSettings) *WallpaperSettings {
	value := func(v int, ok bool) *int {
		if !ok {
			return nil
		}
		return &v
	}
	return &WallpaperSettings{
		BackgroundColor:       value(settings.GetBackgroundColor()),
		SecondBackgroundColor: value(settings.GetSecondBackgroundColor()),
		ThirdBackgroundColor:  value(settings.GetThirdBackgroundColor()),
		FourthBackgroundColor: value(settings.GetFourthBackgroundColor()),
		Intensity:             value(settings.GetIntensity()),
		Rotation:              value(settings.GetRotation()),
		Blur:                  settings.GetBlur(),
		Motion:                settings.GetMotion(),
	}
}

func (t *TelegramClient) convertWallpaper(ctx context.Context, raw tg.WallPaperClass) (Wallpaper, error) {
	switch paper := raw.(type) {
	case *tg.WallPaperNoFile:
		wallpaper := Wallpaper{
			ID:      strconv.FormatInt(paper.ID, 10),
			Default: paper.Default,
			Dark:    paper.Dark,
			Kind:    "fill",
		}
		if settings, ok := paper.GetSettings(); ok {
			wallpaper.Settings = convertWallpaperSettings(settings)
		}
		return wallpaper, nil
	case *tg.WallPaper:
		doc, ok := paper.Document.(*tg.Document)
		if !ok {
			return Wallpaper{}, fmt.Errorf("wallpaper %d has no document", paper.ID)
		}
		wallpaper := Wallpaper{
			ID:      strconv.FormatInt(paper.ID, 10),
			Slug:    paper.Slug,
			Default: paper.Default,
			Dark:    paper.Dark,
			Kind:    "image",
		}
		if paper.Pattern {
			wallpaper.Kind = "pattern"
		}
		if settings, ok := paper.GetSettings(); ok {
			wallpaper.Settings = convertWallpaperSettings(settings)
		}
		var err error
		if wallpaper.URL, err = t.wallpaperURL(ctx, paper, doc, false); err != nil {
			return Wallpaper{}, err
		}
		if len(doc.Thumbs) > 0 {
			if wallpaper.ThumbnailURL, err = t.wallpaperURL(ctx, paper, doc, true); err != nil {
				return Wallpaper{}, err
			}
		}
		return wallpaper, nil
	default:
		return Wallpaper{}, fmt.Errorf("unknown wallpaper type %T", raw)
	}
}

func (t *TelegramClient) wallpaperURL(ctx context.Context, paper *tg.WallPaper, doc *tg.Document, thumbnail bool) (id.ContentURIString, error) {
	mediaID, err := ids.DirectMediaInfo{
		PeerType:  ids.FakePeerTypeWallpaper,
		PeerID:    paper.ID,
		UserID:    t.telegramUserID,
		MessageID: paper.AccessHash, // as for sticker packs, the message ID field carries the access hash
		ID:        doc.ID,
		Thumbnail: thumbnail,
	}.AsMediaID()
	if err != nil {
		return "", err
	}
	return t.main.Bridge.Matrix.GenerateContentURI(ctx, mediaID)
}

// downloadWallpaper serves a wallpaper's file as direct media. A pattern comes out as plain SVG, which browsers
// draw; Telegram keeps it gzipped under its own MIME type.
func (t *TelegramClient) downloadWallpaper(ctx context.Context, transferer *media.Transferer, info ids.DirectMediaInfo) (mediaproxy.GetMediaResponse, error) {
	raw, err := t.client.API().AccountGetWallPaper(ctx, &tg.InputWallPaper{ID: info.PeerID, AccessHash: info.MessageID})
	if err != nil {
		return nil, fmt.Errorf("failed to get wallpaper: %w", err)
	}
	paper, ok := raw.(*tg.WallPaper)
	if !ok {
		return nil, fmt.Errorf("wallpaper %d has no file", info.PeerID)
	}
	doc, ok := paper.Document.(*tg.Document)
	if !ok || doc.ID != info.ID {
		return nil, fmt.Errorf("wallpaper %d no longer has document %d", info.PeerID, info.ID)
	}
	ready := transferer.WithDocument(doc, info.Thumbnail)
	if info.Thumbnail || doc.MimeType != patternMIMEType {
		return ready.ToDirectMediaResponse(ctx)
	}
	data, err := ready.DownloadBytes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to download pattern: %w", err)
	}
	svg, err := gunzipPattern(data)
	if err != nil {
		return nil, err
	}
	return &mediaproxy.GetMediaResponseData{
		Reader:        io.NopCloser(bytes.NewReader(svg)),
		ContentType:   "image/svg+xml",
		ContentLength: int64(len(svg)),
	}, nil
}

// gunzipPattern unpacks a pattern; one that is not gzipped is passed on as it is.
func gunzipPattern(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to read pattern: %w", err)
	}
	defer reader.Close()
	// A pattern is a few hundred kilobytes unpacked; the limit keeps a bad file from filling memory.
	svg, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to unpack pattern: %w", err)
	}
	return svg, nil
}
