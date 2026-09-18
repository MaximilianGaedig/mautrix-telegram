package connector

import (
	"testing"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestBotCommandsFromInfo(t *testing.T) {
	got := botCommandsFromInfo(tg.BotInfo{Commands: []tg.BotCommand{
		{Command: "start", Description: "Start the bot"},
		{Command: "", Description: "ignored"},
		{Command: "help", Description: "Show help"},
	}})
	if len(got) != 2 || got[0].Command != "start" || got[1].Description != "Show help" {
		t.Fatalf("unexpected commands: %+v", got)
	}
}
