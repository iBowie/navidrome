package subsonic

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
)

// GetCanvas serves a song's canvas: a short looping video kept next to the audio file, either
// "<file name without extension>.canvas.mp4" for the song or "canvas.mp4" for its whole folder.
// A song without one answers a plain 404, so clients can treat it like any missing media.
// BowPlayer extension, advertised as "bowCanvas".
func (api *Router) GetCanvas(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	ctx := r.Context()
	id, err := req.Params(r).String("id")
	if err != nil {
		return nil, err
	}
	mf, err := api.ds.MediaFile(ctx).Get(id)
	if errors.Is(err, model.ErrNotFound) {
		http.NotFound(w, r)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	f, err := openCanvas(mf.AbsolutePath())
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	log.Debug(ctx, "Serving canvas", "id", id, "file", f.Name())
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", info.ModTime(), f)
	return nil, nil
}

// openCanvas only ever opens one of the two names derived from the song's own path.
func openCanvas(songPath string) (*os.File, error) {
	dir := filepath.Dir(songPath)
	stem := strings.TrimSuffix(filepath.Base(songPath), filepath.Ext(songPath))
	f, err := os.Open(filepath.Join(dir, stem+".canvas.mp4"))
	if errors.Is(err, os.ErrNotExist) {
		return os.Open(filepath.Join(dir, "canvas.mp4"))
	}
	return f, err
}
