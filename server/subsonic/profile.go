package subsonic

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/persistence"
	"github.com/navidrome/navidrome/server/filter"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/gravatar"
	"github.com/navidrome/navidrome/utils/req"
	"github.com/navidrome/navidrome/utils/slice"
)

// bowProfile: profiles of registered users — public playlists and starred items — readable by any
// signed-in user (every Subsonic call is authenticated). Advertised as the "bowProfile" extension.
// Spec: protocol/profile.md in the BowPlayer repo.

var errUserNotFound = newError(responses.ErrorDataNotFound, "user not found")

// profileUser resolves the "user" param (id or user name, case-insensitive) to a user.
func (api *Router) profileUser(r *http.Request) (*model.User, error) {
	p := req.Params(r)
	nameOrID, err := p.String("user")
	if err != nil {
		return nil, err
	}
	users := api.ds.User(r.Context())
	if u, err := users.Get(nameOrID); err == nil {
		return u, nil
	}
	if u, err := users.FindByUsername(nameOrID); err == nil {
		return u, nil
	}
	return nil, errUserNotFound
}

func profileName(u *model.User) string {
	if u.Name != "" {
		return u.Name
	}
	return u.UserName
}

// profileAvatar mirrors the server's own avatar rule: only when the user has an email.
func profileAvatar(u *model.User) string {
	if u.Email == "" {
		return ""
	}
	return gravatar.Url(u.Email, 160)
}

func writeProfileJSON(w http.ResponseWriter, r *http.Request, v any) (*responses.Subsonic, error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error(r, "bowProfile: encoding response", err)
	}
	return nil, nil
}

// SearchUsers finds registered users by name (user name or display name, case-insensitive
// substring), most recently active first, at most 10. Any signed-in user may search.
func (api *Router) SearchUsers(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	query := strings.ToLower(req.Params(r).StringOr("query", ""))
	users, err := api.ds.User(r.Context()).GetAll()
	if err != nil {
		return nil, err
	}
	type userEntry struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		UserName  string `json:"userName"`
		AvatarURL string `json:"avatarUrl,omitempty"`
	}
	matches := make([]userEntry, 0, 10)
	for _, u := range users {
		if query != "" && !strings.Contains(strings.ToLower(u.UserName), query) &&
			!strings.Contains(strings.ToLower(u.Name), query) {
			continue
		}
		matches = append(matches, userEntry{u.ID, profileName(&u), u.UserName, profileAvatar(&u)})
		if len(matches) == 10 {
			break
		}
	}
	return writeProfileJSON(w, r, map[string]any{"users": matches})
}

// GetUserProfile returns a user's basic profile info.
func (api *Router) GetUserProfile(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	u, err := api.profileUser(r)
	if err != nil {
		return nil, err
	}
	return writeProfileJSON(w, r, map[string]any{
		"id": u.ID, "name": profileName(u), "userName": u.UserName, "avatarUrl": profileAvatar(u),
	})
}

// GetUserPlaylists lists a user's public playlists.
func (api *Router) GetUserPlaylists(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	u, err := api.profileUser(r)
	if err != nil {
		return nil, err
	}
	playlists, err := api.ds.Playlist(r.Context()).GetAll()
	if err != nil {
		return nil, err
	}
	type playlistEntry struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		SongCount int     `json:"songCount"`
		Duration  float32 `json:"duration"`
		CoverArt  string  `json:"coverArt,omitempty"`
	}
	entries := make([]playlistEntry, 0, len(playlists))
	for _, pl := range playlists {
		if pl.OwnerID != u.ID || !pl.Public {
			continue
		}
		e := playlistEntry{ID: pl.ID, Name: pl.Name, SongCount: pl.SongCount, Duration: pl.Duration, CoverArt: pl.ID}
		entries = append(entries, e)
	}
	return writeProfileJSON(w, r, map[string]any{"playlists": entries})
}

// GetUserStarred returns the songs, albums and artists a user starred, filtered by the caller's
// library access: the queries run with the caller's context (library visibility) but the target
// user's annotations (persistence.WithAnnotationUser).
func (api *Router) GetUserStarred(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	u, err := api.profileUser(r)
	if err != nil {
		return nil, err
	}
	ctx := persistence.WithAnnotationUser(r.Context(), u.ID)

	musicFolderIds, err := selectedMusicFolderIds(r, false)
	if err != nil {
		return nil, err
	}

	artistOpts := filter.ApplyArtistLibraryFilter(filter.ArtistsByStarred(), musicFolderIds)
	artists, err := api.ds.Artist(ctx).GetAll(artistOpts)
	if err != nil {
		return nil, err
	}
	albumOpts := filter.ApplyLibraryFilter(filter.ByStarred(), musicFolderIds)
	albums, err := api.ds.Album(ctx).GetAll(albumOpts)
	if err != nil {
		return nil, err
	}
	mediaFiles, err := api.ds.MediaFile(ctx).GetAll(albumOpts)
	if err != nil {
		return nil, err
	}

	return writeProfileJSON(w, r, map[string]any{
		"artists": slice.MapWithArg(artists, r, toArtistID3),
		"albums":  slice.MapWithArg(albums, r.Context(), buildAlbumID3),
		"songs":   slice.MapWithArg(mediaFiles, r.Context(), childFromMediaFile),
	})
}
