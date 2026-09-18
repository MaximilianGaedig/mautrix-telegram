package connector

import (
	"encoding/json"
	"fmt"
	"testing"

	"maunium.net/go/mautrix/id"

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

func TestCollectBotCommands(t *testing.T) {
	infos := []tg.BotInfo{
		{UserID: 3, Commands: []tg.BotCommand{{Command: "zeta", Description: "z"}}},
		{UserID: 1, Commands: []tg.BotCommand{{Command: "start", Description: "Start"}}},
		{UserID: 1, Commands: []tg.BotCommand{{Command: "dup", Description: "ignored"}}},
		{UserID: 2}, // no commands
		{UserID: 0, Commands: []tg.BotCommand{{Command: "anon"}}},
		{UserID: 4, Commands: []tg.BotCommand{{Command: "help"}}},
	}
	users := []tg.UserClass{
		&tg.User{ID: 1, Username: "BravoBot"},
		&tg.User{ID: 3, Usernames: []tg.Username{{Username: "old", Active: false}, {Username: "alphabot", Active: true}}},
		&tg.UserEmpty{ID: 4},
	}
	got := collectBotCommands(infos, users, func(uid int64) id.UserID {
		return id.UserID(fmt.Sprintf("@telegram_%d:example.com", uid))
	})
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %+v", got)
	}
	// No username sorts first, then case-insensitive by username.
	if got[0].Bot != "@telegram_4:example.com" || got[0].Username != "" {
		t.Errorf("entry 0: %+v", got[0])
	}
	if got[1].Username != "alphabot" || got[1].Commands[0].Command != "zeta" {
		t.Errorf("entry 1: %+v", got[1])
	}
	if got[2].Username != "BravoBot" || len(got[2].Commands) != 1 || got[2].Commands[0].Command != "start" {
		t.Errorf("entry 2: %+v", got[2])
	}
}

func TestBotCommandsContentJSON(t *testing.T) {
	group, _ := json.Marshal(&BotCommandsContent{Bots: []BotCommandsEntry{}})
	if string(group) != `{"bots":[]}` {
		t.Errorf("group content: %s", group)
	}
	dm, _ := json.Marshal(&BotCommandsContent{Bot: "@b:x", Commands: []BotCommand{}, Bots: []BotCommandsEntry{}})
	if string(dm) != `{"bot":"@b:x","commands":[],"bots":[]}` {
		t.Errorf("dm content: %s", dm)
	}
	if botCommandsStateHash("!a:x", &BotCommandsContent{}) == botCommandsStateHash("!b:x", &BotCommandsContent{}) {
		t.Error("state hash should depend on the room")
	}
}
