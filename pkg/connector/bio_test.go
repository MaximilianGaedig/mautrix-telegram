package connector

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/util/jsontime"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

var bioNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func addressableUser(id int64) *tg.User {
	user := &tg.User{ID: id}
	user.SetAccessHash(777)
	return user
}

type fakeFullUsers struct {
	about string
	err   error
	asked []tg.InputUserClass
}

func (f *fakeFullUsers) UsersGetFullUser(_ context.Context, id tg.InputUserClass) (*tg.UsersUserFull, error) {
	f.asked = append(f.asked, id)
	if f.err != nil {
		return nil, f.err
	}
	return &tg.UsersUserFull{FullUser: tg.UserFull{About: f.about}}, nil
}

func TestBioDue(t *testing.T) {
	minUser := addressableUser(1)
	minUser.Min = true
	deleted := addressableUser(1)
	deleted.Deleted = true
	tests := []struct {
		name string
		meta GhostMetadata
		user *tg.User
		want bool
	}{
		{"never fetched", GhostMetadata{}, addressableUser(1), true},
		{"fetched an hour ago", GhostMetadata{BioFetched: jsontime.U(bioNow.Add(-time.Hour))}, addressableUser(1), false},
		{"fetched just under a day ago", GhostMetadata{BioFetched: jsontime.U(bioNow.Add(-23 * time.Hour))}, addressableUser(1), false},
		{"fetched a day ago", GhostMetadata{BioFetched: jsontime.U(bioNow.Add(-24 * time.Hour))}, addressableUser(1), true},
		{"min user", GhostMetadata{}, minUser, false},
		{"deleted user", GhostMetadata{}, deleted, false},
		{"no access hash", GhostMetadata{}, &tg.User{ID: 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bioDue(&tt.meta, tt.user, bioNow))
		})
	}
}

func rawBio(t *testing.T, profile database.ExtraProfile) string {
	t.Helper()
	raw, ok := profile[bioProfileKey]
	require.True(t, ok, "no bio key in %v", profile)
	return string(raw)
}

func TestBioProfile(t *testing.T) {
	t.Run("bio set", func(t *testing.T) {
		assert.Equal(t, `"Hi, I make things"`, rawBio(t, bioProfile("Hi, I make things", nil)))
	})
	t.Run("bio cleared when there was one", func(t *testing.T) {
		current := database.ExtraProfile{bioProfileKey: json.RawMessage(`"old"`)}
		assert.Equal(t, "null", rawBio(t, bioProfile("", current)))
	})
	t.Run("no bio and none before writes nothing", func(t *testing.T) {
		assert.Nil(t, bioProfile("", nil))
	})
	t.Run("no bio and a cleared one before writes nothing", func(t *testing.T) {
		current := database.ExtraProfile{bioProfileKey: json.RawMessage(`null`)}
		assert.Nil(t, bioProfile("", current))
	})
}

func TestBioUpdateFetchesOncePerDay(t *testing.T) {
	api := &fakeFullUsers{about: "Hello"}
	meta := &GhostMetadata{}
	user := addressableUser(42)

	profile, attempted := bioUpdate(context.Background(), api, meta, user, nil, bioNow)
	assert.True(t, attempted)
	assert.Equal(t, `"Hello"`, rawBio(t, profile))
	require.Len(t, api.asked, 1)
	assert.Equal(t, &tg.InputUser{UserID: 42, AccessHash: 777}, api.asked[0])

	// The caller records the attempt; a sync an hour later must not fetch again
	meta.BioFetched = jsontime.U(bioNow)
	profile, attempted = bioUpdate(context.Background(), api, meta, user, nil, bioNow.Add(time.Hour))
	assert.False(t, attempted)
	assert.Nil(t, profile)
	assert.Len(t, api.asked, 1)

	// A day later it does
	_, attempted = bioUpdate(context.Background(), api, meta, user, nil, bioNow.Add(25*time.Hour))
	assert.True(t, attempted)
	assert.Len(t, api.asked, 2)
}

func TestBioUpdateFailureIsAnAttemptWithoutProfile(t *testing.T) {
	api := &fakeFullUsers{err: errors.New("flood")}
	profile, attempted := bioUpdate(context.Background(), api, &GhostMetadata{}, addressableUser(42), nil, bioNow)
	assert.True(t, attempted, "a failed fetch is still recorded so it is not retried on every sync")
	assert.Nil(t, profile)
}

func TestBioUpdateSkipsMinUsers(t *testing.T) {
	api := &fakeFullUsers{about: "Hello"}
	user := addressableUser(42)
	user.Min = true
	_, attempted := bioUpdate(context.Background(), api, &GhostMetadata{}, user, nil, bioNow)
	assert.False(t, attempted)
	assert.Empty(t, api.asked)
}

func TestClaimBioFetch(t *testing.T) {
	tc := &TelegramClient{}
	assert.True(t, tc.claimBioFetch(1, bioNow))
	assert.False(t, tc.claimBioFetch(1, bioNow.Add(time.Second)), "concurrent update of the same ghost")
	assert.True(t, tc.claimBioFetch(2, bioNow), "another user")
	assert.True(t, tc.claimBioFetch(1, bioNow.Add(25*time.Hour)))
}
