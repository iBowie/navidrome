package subsonic

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

// bowTypes: custom types for playlists and albums — compilation, soundtrack, single, or any label
// the library owner wants. The assignments live in the data volume ("types.json") so they can be
// edited or generated there directly:
//
//	{"playlists": {"<playlist id>": "soundtrack"}, "albums": {"<album id>": "single"}}
//
// Clients show the type as a badge or group by it; the DB and scanner are untouched. Advertised
// as the "bowTypes" extension. Both the Subsonic handlers below and the native API's library-data
// routes (nativeapi/librarydata.go) use these Read/Write cores.

func typesFilePath() string {
	return filepath.Join(conf.Server.DataFolder.String(), "types.json")
}

// ReadTypes answers the current types document, empty maps when none was written yet.
func ReadTypes() (map[string]string, map[string]string, error) {
	data, err := os.ReadFile(typesFilePath())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, map[string]string{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	// Validate before answering, so a hand-edited mistake answers an error instead of broken clients.
	var doc struct {
		Playlists map[string]string `json:"playlists"`
		Albums    map[string]string `json:"albums"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Playlists == nil {
		doc.Playlists = map[string]string{}
	}
	if doc.Albums == nil {
		doc.Albums = map[string]string{}
	}
	return doc.Playlists, doc.Albums, nil
}

// WriteTypes replaces the whole types document; data must be a valid types JSON document.
func WriteTypes(data []byte) error {
	var doc struct {
		Playlists map[string]string `json:"playlists"`
		Albums    map[string]string `json:"albums"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	return writeJSONAtomic(typesFilePath(), data)
}

// GetTypes serves the whole types file as plain JSON. A missing file answers empty maps.
func (api *Router) GetTypes(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	playlists, albums, err := ReadTypes()
	if err != nil {
		return nil, newError(responses.ErrorGeneric, "types.json is not valid: "+err.Error())
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	return writeProfileJSON(w, r, map[string]any{"playlists": playlists, "albums": albums})
}

// SaveTypes (admin) replaces the whole types file with the raw JSON body (validated first).
func (api *Router) SaveTypes(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		return nil, newError(responses.ErrorGeneric, "reading body: "+err.Error())
	}
	if err := WriteTypes(data); err != nil {
		return nil, newError(responses.ErrorGeneric, "body must be a types document: "+err.Error())
	}
	log.Info(r, "Custom types saved", "size", len(data))
	return nil, nil
}
