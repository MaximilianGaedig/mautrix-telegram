package connector

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// bioProfileKey is the extra profile field a ghost's Telegram bio is put in: MSC4440's biography field
// (unstable name), which Matrix clients read the same for every network.
const bioProfileKey = "gay.fomx.biography"

// bioRefreshInterval is how often at most a user's bio is fetched. Bios come from the full user, which is a
// separate (and rate limited) request per user, so they are not fetched on every ghost sync.
const bioRefreshInterval = 24 * time.Hour

// fullUserGetter is the part of the Telegram API that returns a user's full info; it is *tg.Client.
type fullUserGetter interface {
	UsersGetFullUser(ctx context.Context, id tg.InputUserClass) (*tg.UsersUserFull, error)
}

// bioDue says whether the user's bio should be fetched now: only for real (not min) users we can address,
// and only once per refresh interval, counted from the last attempt saved in the ghost's metadata.
func bioDue(meta *GhostMetadata, user *tg.User, now time.Time) bool {
	if user.Min || user.Deleted {
		return false
	}
	if _, ok := user.GetAccessHash(); !ok {
		return false
	}
	return meta.BioFetched.IsZero() || now.Sub(meta.BioFetched.Time) >= bioRefreshInterval
}

// bioProfile is the profile update for a bio. An empty bio clears the field, but only if there is one to
// clear, so users without a bio never get a field written.
func bioProfile(about string, current database.ExtraProfile) database.ExtraProfile {
	profile := database.ExtraProfile{}
	if about != "" {
		// MSC4440 keeps the text in extensible events' m.text form.
		if err := profile.Set(bioProfileKey, map[string]any{"m.text": []map[string]string{{"body": about}}}); err != nil {
			return nil
		}
		return profile
	}
	if existing, ok := current[bioProfileKey]; !ok || string(existing) == "null" {
		return nil
	}
	if err := profile.Set(bioProfileKey, nil); err != nil {
		return nil
	}
	return profile
}

// bioUpdate fetches the user's bio if it is due. attempted says a request was made, which the caller
// records so the next attempt waits for the interval, whether it worked or not. profile is nil when there is
// nothing to change.
func bioUpdate(
	ctx context.Context, api fullUserGetter, meta *GhostMetadata, user *tg.User, current database.ExtraProfile, now time.Time,
) (profile database.ExtraProfile, attempted bool) {
	if !bioDue(meta, user, now) {
		return nil, false
	}
	accessHash, _ := user.GetAccessHash()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	full, err := api.UsersGetFullUser(ctx, &tg.InputUser{UserID: user.ID, AccessHash: accessHash})
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Int64("user_id", user.ID).Msg("Failed to get the bio of a user")
		return nil, true
	}
	return bioProfile(full.FullUser.About, current), true
}

// claimBioFetch stops concurrent updates of the same ghost from each fetching the bio before the first one
// has saved that it did.
func (tc *TelegramClient) claimBioFetch(userID int64, now time.Time) bool {
	tc.bioClaimLock.Lock()
	defer tc.bioClaimLock.Unlock()
	if last, ok := tc.bioClaims[userID]; ok && now.Sub(last) < bioRefreshInterval {
		return false
	}
	if tc.bioClaims == nil {
		tc.bioClaims = make(map[int64]time.Time)
	}
	tc.bioClaims[userID] = now
	return true
}
