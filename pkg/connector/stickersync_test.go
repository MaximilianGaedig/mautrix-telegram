package connector

import (
	"slices"
	"testing"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestStickerSetSyncDiff(t *testing.T) {
	installed := []tg.StickerSet{
		{ID: 1, ShortName: "unchanged", Hash: 10},
		{ID: 2, ShortName: "changed", Hash: 21},
		{ID: 3, ShortName: "new", Hash: 30},
		{ID: 3, ShortName: "new", Hash: 30}, // duplicate across sticker/emoji lists
		{ID: 5, ShortName: "renamed2", Hash: 50},
		{ID: 6, ShortName: "", Hash: 60}, // no state key possible
	}
	synced := map[string]SyncedStickerPack{
		"1": {ShortName: "unchanged", Hash: 10},
		"2": {ShortName: "changed", Hash: 20},
		"4": {ShortName: "uninstalled", Hash: 40},
		"5": {ShortName: "renamed", Hash: 50},
	}
	toImport, toRemove := stickerSetSyncDiff(installed, synced)
	var names []string
	for _, set := range toImport {
		names = append(names, set.ShortName)
	}
	if !slices.Equal(names, []string{"changed", "new", "renamed2"}) {
		t.Errorf("unexpected imports: %v", names)
	}
	if !slices.Equal(toRemove, []string{"4"}) {
		t.Errorf("unexpected removals: %v", toRemove)
	}

	toImport, toRemove = stickerSetSyncDiff(nil, nil)
	if len(toImport) != 0 || len(toRemove) != 0 {
		t.Errorf("empty diff not empty: %v %v", toImport, toRemove)
	}
}
