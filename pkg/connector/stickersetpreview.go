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
	"context"
	"fmt"
	"image"
	"image/png"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/image/draw"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-telegram/pkg/connector/media"
	"go.mau.fi/mautrix-telegram/pkg/connector/store"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

const (
	// stickerSetPreviewMax is how many stickers go into the link preview grid (2x2).
	stickerSetPreviewMax = 4
	// stickerSetPreviewCell is the side of one grid cell in pixels.
	stickerSetPreviewCell = 128
)

// webPageStickerSet is the sticker set shown by a t.me/addstickers or t.me/addemoji
// web page preview.
type webPageStickerSet struct {
	URL       string
	ShortName string
	Title     string
	Emojis    bool
	// Docs are the first few stickers that have a thumbnail to build the preview from.
	Docs []*tg.Document
}

// stickerSetFromWebPage extracts the sticker set from a web page preview, or returns nil
// if the page isn't a sticker set preview with stickers.
func stickerSetFromWebPage(webpage *tg.WebPage) *webPageStickerSet {
	if webpage == nil {
		return nil
	}
	for _, rawAttr := range webpage.Attributes {
		attr, ok := rawAttr.(*tg.WebPageAttributeStickerSet)
		if !ok || len(attr.Stickers) == 0 {
			continue
		}
		out := &webPageStickerSet{
			URL:    webpage.URL,
			Title:  webpage.Title,
			Emojis: attr.Emojis,
		}
		if match := addStickersRegex.FindStringSubmatch(webpage.URL); match != nil {
			out.ShortName = match[1]
		}
		if out.Title == "" {
			out.Title = out.ShortName
		}
		for _, rawDoc := range attr.Stickers {
			doc, ok := rawDoc.(*tg.Document)
			if !ok || len(doc.Thumbs) == 0 {
				continue
			}
			out.Docs = append(out.Docs, doc)
			if len(out.Docs) >= stickerSetPreviewMax {
				break
			}
		}
		if len(out.Docs) == 0 {
			return nil
		}
		return out
	}
	return nil
}

func (s *webPageStickerSet) description(fallback string) string {
	if fallback != "" {
		return fallback
	}
	if s.Emojis {
		return "Telegram custom emoji pack"
	}
	return "Telegram sticker pack"
}

// cacheKey identifies the rendered grid for the media cache: the same stickers always
// produce the same image.
func (s *webPageStickerSet) cacheKey() store.TelegramFileLocationID {
	parts := make([]string, len(s.Docs))
	for i, doc := range s.Docs {
		parts[i] = strconv.FormatInt(doc.ID, 10)
	}
	return store.TelegramFileLocationID("stickerset_preview_" + strings.Join(parts, "_"))
}

// composeStickerGrid draws up to four images into a transparent 2x2 grid (or a single
// cell for one image, a 2x1 strip for two), each scaled to fit a cell.
func composeStickerGrid(images []image.Image, cell int) *image.RGBA {
	if len(images) > stickerSetPreviewMax {
		images = images[:stickerSetPreviewMax]
	}
	cols, rows := 2, 2
	switch len(images) {
	case 0:
		return image.NewRGBA(image.Rect(0, 0, cell, cell))
	case 1:
		cols, rows = 1, 1
	case 2:
		cols, rows = 2, 1
	}
	out := image.NewRGBA(image.Rect(0, 0, cols*cell, rows*cell))
	for i, img := range images {
		scaled := resizeEmoji(img, cell)
		x, y := (i%cols)*cell, (i/cols)*cell
		draw.Draw(out, image.Rect(x, y, x+cell, y+cell), scaled, image.Point{}, draw.Over)
	}
	return out
}

// dedupeLinkPreviews drops later previews whose URL was already previewed.
func dedupeLinkPreviews(previews []*event.BeeperLinkPreview) []*event.BeeperLinkPreview {
	if len(previews) < 2 {
		return previews
	}
	seen := make(map[string]struct{}, len(previews))
	out := previews[:0]
	for _, p := range previews {
		if p == nil {
			continue
		}
		key := p.MatchedURL
		if key == "" {
			key = p.CanonicalURL
		}
		if _, dup := seen[key]; dup && key != "" {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

// fillStickerSetPreview replaces the link card image with a grid of the set's first
// stickers. On failure the preview keeps its plain title/description.
func (tc *TelegramClient) fillStickerSetPreview(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, preview *event.BeeperLinkPreview, set *webPageStickerSet) error {
	preview.Title = set.Title
	preview.Description = set.description(preview.Description)
	preview.SiteName = "Telegram"

	cacheKey := set.cacheKey()
	if cached, err := tc.main.Store.TelegramFile.GetByLocationID(ctx, cacheKey); err != nil {
		return fmt.Errorf("failed to check preview cache: %w", err)
	} else if cached != nil {
		setPreviewImage(preview, cached.MXC, nil, cached.Size, cached.Width, cached.Height, cached.MIMEType)
		return nil
	}

	images := make([]image.Image, len(set.Docs))
	var wg sync.WaitGroup
	for i, doc := range set.Docs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := media.NewTransferer(tc.client.API()).WithDocument(doc, true).DownloadBytes(ctx)
			if err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Int64("document_id", doc.ID).Msg("Failed to download sticker thumbnail for preview")
				return
			}
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Int64("document_id", doc.ID).Msg("Failed to decode sticker thumbnail for preview")
				return
			}
			images[i] = img
		}()
	}
	wg.Wait()
	decoded := images[:0]
	for _, img := range images {
		if img != nil {
			decoded = append(decoded, img)
		}
	}
	if len(decoded) == 0 {
		return fmt.Errorf("no sticker thumbnails could be decoded")
	}
	grid := composeStickerGrid(decoded, stickerSetPreviewCell)
	var buf bytes.Buffer
	if err := png.Encode(&buf, grid); err != nil {
		return fmt.Errorf("failed to encode preview grid: %w", err)
	}
	mxc, encrypted, err := intent.UploadMedia(ctx, portal.MXID, buf.Bytes(), "stickers.png", "image/png")
	if err != nil {
		return fmt.Errorf("failed to upload preview grid: %w", err)
	}
	width, height := grid.Bounds().Dx(), grid.Bounds().Dy()
	setPreviewImage(preview, mxc, encrypted, buf.Len(), width, height, "image/png")
	if encrypted == nil {
		err = tc.main.Store.TelegramFile.Insert(ctx, &store.TelegramFile{
			LocationID: cacheKey,
			MXC:        mxc,
			MIMEType:   "image/png",
			Size:       buf.Len(),
			Width:      width,
			Height:     height,
			Timestamp:  time.Now(),
		})
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to cache sticker set preview")
		}
	}
	return nil
}

func setPreviewImage(preview *event.BeeperLinkPreview, mxc id.ContentURIString, encrypted *event.EncryptedFileInfo, size, width, height int, mime string) {
	preview.ImageURL = mxc
	preview.ImageEncryption = encrypted
	preview.ImageSize = event.IntOrString(size)
	preview.ImageWidth = event.IntOrString(width)
	preview.ImageHeight = event.IntOrString(height)
	preview.ImageType = mime
}
