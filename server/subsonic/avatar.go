package subsonic

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // avatar formats
	_ "image/jpeg" //
	_ "image/png"  //
	_ "golang.org/x/image/webp"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

// bowAvatar: a user's own avatar picture, uploaded and replaced by the user (any signed-in user may
// change only their own). The image lives in the data volume, under "avatars/<user id>.<format>",
// so it survives restarts and needs no DB change; GetAvatar serves it before the Gravatar fallback
// and the profile endpoints report it as "hasAvatar". Advertised as the "bowAvatar" extension.

const maxAvatarBytes = 5 << 20 // uploads above this are rejected before decoding

func avatarDir() string {
	return filepath.Join(conf.Server.DataFolder.String(), "avatars")
}

// avatarFile finds the calling user's uploaded avatar, whatever format it was saved in.
func avatarFile(userID string) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(avatarDir(), userID+".*"))
	if err != nil {
		return "", false
	}
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.Mode().IsRegular() {
			return m, true
		}
	}
	return "", false
}

func hasAvatar(userID string) bool {
	_, ok := avatarFile(userID)
	return ok
}

// SaveAvatar stores the multipart "avatar" file as the calling user's avatar. Any image format
// image.DecodeConfig understands is accepted; the bytes are kept as uploaded.
func (api *Router) SaveAvatar(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	user, ok := request.UserFrom(r.Context())
	if !ok {
		return nil, newError(responses.ErrorGeneric, "no user in context")
	}
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		return nil, newError(responses.ErrorGeneric, fmt.Sprintf("reading upload: %v", err))
	}
	file, _, err := r.FormFile("avatar")
	if err != nil {
		return nil, newError(responses.ErrorMissingParameter, "missing multipart file field \"avatar\"")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAvatarBytes {
		return nil, newError(responses.ErrorGeneric, "avatar is too large (max 5 MB)")
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, newError(responses.ErrorGeneric, "avatar must be an image")
	}

	if err := os.MkdirAll(avatarDir(), 0755); err != nil {
		return nil, err
	}
	// Drop the previous avatar (maybe another format) so only one file per user remains.
	api.deleteAvatarFiles(user.ID)
	path := filepath.Join(avatarDir(), user.ID+"."+format)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	log.Info(r, "Avatar uploaded", "user", user.UserName, "size", len(data))
	return nil, nil
}

func (api *Router) deleteAvatarFiles(userID string) {
	if old, ok := avatarFile(userID); ok {
		_ = os.Remove(old)
	}
}

// DeleteAvatar removes the calling user's uploaded avatar, falling back to Gravatar/placeholder.
func (api *Router) DeleteAvatar(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	user, ok := request.UserFrom(r.Context())
	if !ok {
		return nil, newError(responses.ErrorGeneric, "no user in context")
	}
	api.deleteAvatarFiles(user.ID)
	return nil, nil
}

// serveAvatar serves the user's uploaded avatar when there is one; ok is false when there isn't.
func serveAvatar(w http.ResponseWriter, r *http.Request, userID string) (ok bool, err error) {
	path, found := avatarFile(userID)
	if !found {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	log.Debug(r, "Serving uploaded avatar", "user", userID, "file", path)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
	return true, nil
}
