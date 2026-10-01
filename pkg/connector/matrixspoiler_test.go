// mautrix-telegram - A Matrix-Telegram puppeting bridge.
// Copyright (C) 2026 Sumner Evans
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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestMatrixMediaHasSpoiler(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"plain image", `{"msgtype":"m.image","body":"a.jpg","info":{"mimetype":"image/jpeg"}}`, false},
		{"msc4193 unstable", `{"msgtype":"m.image","page.codeberg.everypizza.msc4193.spoiler":true}`, true},
		{"msc4193 unstable set to false", `{"msgtype":"m.image","page.codeberg.everypizza.msc4193.spoiler":false}`, false},
		{"msc4193 stable", `{"msgtype":"m.video","m.spoiler":true}`, true},
		{"msc3725 content warning", `{"msgtype":"m.image","town.robin.msc3725.content_warning":{"type":"town.robin.msc3725.spoiler"}}`, true},
		{"msc3725 empty warning", `{"msgtype":"m.image","town.robin.msc3725.content_warning":{}}`, false},
		{"marker written by this bridge", `{"msgtype":"m.image","info":{"fi.mau.telegram.spoiler":true}}`, true},
		{"spoiler of the wrong type is ignored", `{"msgtype":"m.image","m.spoiler":"yes"}`, false},
		{
			"edit that adds a spoiler",
			`{"msgtype":"m.image","m.new_content":{"msgtype":"m.image","page.codeberg.everypizza.msc4193.spoiler":true}}`,
			true,
		},
		{
			"edit that removes a spoiler",
			`{"msgtype":"m.image","page.codeberg.everypizza.msc4193.spoiler":true,"m.new_content":{"msgtype":"m.image"}}`,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.content), &raw))
			assert.Equal(t, tt.want, matrixMediaHasSpoiler(raw))
		})
	}
	assert.False(t, matrixMediaHasSpoiler(nil))
}

func TestMakeUploadedMediaSpoiler(t *testing.T) {
	upload := &tg.InputFile{ID: 1, Parts: 1, Name: "upload"}
	tests := []struct {
		name          string
		msgType       event.MessageType
		info          event.FileInfo
		sticker       bool
		forceDocument bool
		wantPhoto     bool
		wantSpoiler   bool
	}{
		{name: "photo", msgType: event.MsgImage, info: event.FileInfo{MimeType: "image/jpeg"}, wantPhoto: true, wantSpoiler: true},
		{name: "video", msgType: event.MsgVideo, info: event.FileInfo{MimeType: "video/mp4"}, wantSpoiler: true},
		{name: "video gif", msgType: event.MsgVideo, info: event.FileInfo{MimeType: "video/mp4", MauGIF: true}, wantSpoiler: true},
		{name: "image gif", msgType: event.MsgImage, info: event.FileInfo{MimeType: "image/gif"}, wantSpoiler: true},
		// The rest are sent as plain documents, which Telegram has no way to blur.
		{name: "image sent as file", msgType: event.MsgImage, info: event.FileInfo{MimeType: "image/jpeg"}, forceDocument: true},
		{name: "file", msgType: event.MsgFile, info: event.FileInfo{MimeType: "application/pdf"}},
		{name: "audio", msgType: event.MsgAudio, info: event.FileInfo{MimeType: "audio/ogg"}},
		{name: "sticker", msgType: event.MsgImage, info: event.FileInfo{MimeType: "image/webp"}, sticker: true},
	}
	spoilerOf := func(t *testing.T, media tg.InputMediaClass, wantPhoto bool) bool {
		switch typed := media.(type) {
		case *tg.InputMediaUploadedPhoto:
			require.True(t, wantPhoto, "expected a document, got a photo")
			return typed.Spoiler
		case *tg.InputMediaUploadedDocument:
			require.False(t, wantPhoto, "expected a photo, got a document")
			return typed.Spoiler
		default:
			require.Failf(t, "unexpected media type", "%T", media)
			return false
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := &event.MessageEventContent{MsgType: tt.msgType, Body: "media", Info: &tt.info}
			withSpoiler := makeUploadedMedia(upload, content, content.Info, "media", tt.sticker, tt.forceDocument, true)
			assert.Equal(t, tt.wantSpoiler, spoilerOf(t, withSpoiler, tt.wantPhoto))
			withoutSpoiler := makeUploadedMedia(upload, content, content.Info, "media", tt.sticker, tt.forceDocument, false)
			assert.False(t, spoilerOf(t, withoutSpoiler, tt.wantPhoto), "media must not be hidden unless the event asks for it")
		})
	}
}
