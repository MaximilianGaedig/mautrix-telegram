package connector

import (
	"image"
	"image/color"
	"testing"

	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func stickerDoc(id int64, thumbs bool) *tg.Document {
	doc := &tg.Document{ID: id}
	if thumbs {
		doc.Thumbs = []tg.PhotoSizeClass{&tg.PhotoSize{Type: "m", W: 128, H: 128}}
	}
	return doc
}

func TestStickerSetFromWebPage(t *testing.T) {
	if stickerSetFromWebPage(&tg.WebPage{URL: "https://example.com", Title: "x"}) != nil {
		t.Fatal("plain web page detected as sticker set")
	}
	emptySet := &tg.WebPage{
		URL:        "https://t.me/addstickers/Empty",
		Attributes: []tg.WebPageAttributeClass{&tg.WebPageAttributeStickerSet{}},
	}
	if stickerSetFromWebPage(emptySet) != nil {
		t.Fatal("sticker set attribute without stickers should be ignored")
	}

	page := &tg.WebPage{
		URL: "https://t.me/addemoji/CoolEmoji",
		Attributes: []tg.WebPageAttributeClass{
			&tg.WebPageAttributeTheme{},
			&tg.WebPageAttributeStickerSet{Emojis: true, Stickers: []tg.DocumentClass{
				stickerDoc(1, true), stickerDoc(2, false), &tg.DocumentEmpty{ID: 3},
				stickerDoc(4, true), stickerDoc(5, true), stickerDoc(6, true), stickerDoc(7, true),
			}},
		},
	}
	set := stickerSetFromWebPage(page)
	if set == nil {
		t.Fatal("sticker set not detected")
	}
	if set.ShortName != "CoolEmoji" || set.Title != "CoolEmoji" || !set.Emojis {
		t.Errorf("unexpected set info: %+v", set)
	}
	var ids []int64
	for _, d := range set.Docs {
		ids = append(ids, d.ID)
	}
	if len(ids) != 4 || ids[0] != 1 || ids[1] != 4 || ids[3] != 6 {
		t.Errorf("unexpected preview docs: %v", ids)
	}
	if set.cacheKey() != "stickerset_preview_1_4_5_6" {
		t.Errorf("unexpected cache key %q", set.cacheKey())
	}
	if set.description("") != "Telegram custom emoji pack" || set.description("desc") != "desc" {
		t.Error("unexpected description")
	}
}

func solid(c color.Color, w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestComposeStickerGrid(t *testing.T) {
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	cases := []struct {
		n, w, h int
	}{{1, 64, 64}, {2, 128, 64}, {3, 128, 128}, {4, 128, 128}, {6, 128, 128}}
	for _, c := range cases {
		imgs := make([]image.Image, c.n)
		for i := range imgs {
			imgs[i] = solid(red, 200, 100)
		}
		imgs[0] = solid(blue, 50, 50)
		grid := composeStickerGrid(imgs, 64)
		if grid.Bounds().Dx() != c.w || grid.Bounds().Dy() != c.h {
			t.Errorf("n=%d: size %v", c.n, grid.Bounds())
		}
		if got := grid.RGBAAt(32, 32); got != blue {
			t.Errorf("n=%d: first cell center = %v", c.n, got)
		}
		if c.n >= 2 {
			// Wide sticker is letterboxed: center is filled, top edge transparent.
			if got := grid.RGBAAt(96, 32); got != red {
				t.Errorf("n=%d: second cell center = %v", c.n, got)
			}
			if got := grid.RGBAAt(96, 2); got.A != 0 {
				t.Errorf("n=%d: letterbox not transparent: %v", c.n, got)
			}
		}
	}
}

func TestDedupeLinkPreviews(t *testing.T) {
	mk := func(url string) *event.BeeperLinkPreview {
		return &event.BeeperLinkPreview{MatchedURL: url}
	}
	a, a2, b := mk("https://t.me/addstickers/A"), mk("https://t.me/addstickers/A"), mk("https://b")
	got := dedupeLinkPreviews([]*event.BeeperLinkPreview{a, b, a2, nil})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Errorf("unexpected dedupe result: %v", got)
	}
}
