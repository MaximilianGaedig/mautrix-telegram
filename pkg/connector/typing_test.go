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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestTelegramTypingAction(t *testing.T) {
	assert.IsType(t, &tg.SendMessageTypingAction{}, telegramTypingAction(bridgev2.TypingTypeText, true))
	assert.IsType(t, &tg.SendMessageRecordAudioAction{}, telegramTypingAction(bridgev2.TypingTypeRecordingMedia, true))
	assert.IsType(t, &tg.SendMessageUploadDocumentAction{}, telegramTypingAction(bridgev2.TypingTypeUploadingMedia, true))
	// A type this bridge doesn't know yet must still produce an action, the request is invalid
	// without one.
	assert.IsType(t, &tg.SendMessageTypingAction{}, telegramTypingAction(bridgev2.TypingType(99), true))

	for _, typingType := range []bridgev2.TypingType{bridgev2.TypingTypeText, bridgev2.TypingTypeRecordingMedia, bridgev2.TypingTypeUploadingMedia} {
		assert.IsType(t, &tg.SendMessageCancelAction{}, telegramTypingAction(typingType, false))
	}
}

func TestTypingFromTelegramAction(t *testing.T) {
	type result struct {
		typingType bridgev2.TypingType
		typing     bool
		ok         bool
	}
	text := result{bridgev2.TypingTypeText, true, true}
	recording := result{bridgev2.TypingTypeRecordingMedia, true, true}
	uploading := result{bridgev2.TypingTypeUploadingMedia, true, true}
	stopped := result{bridgev2.TypingTypeText, false, true}
	ignored := result{bridgev2.TypingTypeText, false, false}

	tests := []struct {
		action tg.SendMessageActionClass
		want   result
	}{
		{&tg.SendMessageTypingAction{}, text},
		{&tg.SendMessageChooseStickerAction{}, text},
		{&tg.SendMessageGeoLocationAction{}, text},
		{&tg.SendMessageChooseContactAction{}, text},
		{&tg.SendMessageTextDraftAction{}, text},
		{&tg.SendMessageRichMessageDraftAction{}, text},
		{&tg.SendMessageRecordAudioAction{}, recording},
		{&tg.SendMessageRecordVideoAction{}, recording},
		{&tg.SendMessageRecordRoundAction{}, recording},
		{&tg.SendMessageUploadAudioAction{}, uploading},
		{&tg.SendMessageUploadVideoAction{}, uploading},
		{&tg.SendMessageUploadRoundAction{}, uploading},
		{&tg.SendMessageUploadPhotoAction{}, uploading},
		{&tg.SendMessageUploadDocumentAction{}, uploading},
		{&tg.SendMessageCancelAction{}, stopped},
		{&tg.SendMessageGamePlayAction{}, stopped},
		{&tg.SendMessageHistoryImportAction{}, stopped},
		{&tg.SendMessageEmojiInteraction{}, ignored},
		{&tg.SendMessageEmojiInteractionSeen{}, ignored},
		{&tg.SpeakingInGroupCallAction{}, ignored},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%T", tt.action), func(t *testing.T) {
			typingType, timeout, ok := typingFromTelegramAction(tt.action)
			assert.Equal(t, tt.want, result{typingType, timeout > 0, ok})
		})
	}
}
