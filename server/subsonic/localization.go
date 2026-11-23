package subsonic

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
)

// bowLocale: localized tags per language, one small JSON file per entity so editing and syncing
// scale to big libraries:
//
//	localization/<lang>/songs/<song id>.json    {"title": "…", "artist": "…", "lyrics": "…"}
//	localization/<lang>/albums/<album id>.json  {"name": "…", "artist": "…"}
//	localization/<lang>/artists/<artist id>.json {"name": "…"}
//
// Clients don't fetch these files: they send lang=<lang> on ordinary Subsonic calls and the server
// rewrites titles/artists/lyrics in the JSON envelope before it is sent. An optional
// "localization/default.json" ({"lang": "en"}) names the language the embedded tags are in.
// Admins edit through saveLocalization/deleteLocalization. Advertised as "bowLocale".

const localizationTTL = 30 * time.Second

func localizationDir() string {
	return filepath.Join(conf.Server.DataFolder.String(), "localization")
}

func validLang(lang string) bool {
	return lang != "" && !strings.ContainsAny(lang, `/\`) && !strings.Contains(lang, "..")
}

// entityKind is a folder under localization/<lang>/; its files hold the overridable fields.
var entityKinds = map[string]bool{"songs": true, "albums": true, "artists": true}

func localizationEntityDir(lang, kind string) string {
	return filepath.Join(localizationDir(), lang, kind)
}

// ── The per-language in-memory view, refreshed lazily and TTL-capped.

type localizedFields map[string]string // e.g. {"title": …, "artist": …}

type langView struct {
	loadedAt time.Time
	entities map[string]map[string]localizedFields // kind -> id -> fields
}

type localizationStore struct {
	mu    sync.Mutex
	langs map[string]*langView
}

var localization localizationStore

func (s *localizationStore) view(lang string) *langView {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.langs == nil {
		s.langs = map[string]*langView{}
	}
	if v, ok := s.langs[lang]; ok && time.Since(v.loadedAt) < localizationTTL {
		return v
	}
	v := &langView{entities: map[string]map[string]localizedFields{}}
	for kind := range entityKinds {
		byID := map[string]localizedFields{}
		entries, err := os.ReadDir(localizationEntityDir(lang, kind))
		if err != nil {
			continue // no such language/kind: empty
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok {
				continue
			}
			data, err := os.ReadFile(filepath.Join(localizationEntityDir(lang, kind), e.Name()))
			if err != nil {
				continue
			}
			var fields localizedFields
			if json.Unmarshal(data, &fields) == nil && len(fields) > 0 {
				byID[id] = fields
			}
		}
		v.entities[kind] = byID
	}
	s.langs[lang] = v
	return v
}

// drop forgets a language so the next view() reloads it (called after a save/delete).
func (s *localizationStore) drop(lang string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.langs, lang)
}

// ── Response rewriting: every JSON envelope passes through here when the client sent lang=.

// localizeJSON rewrites titles/artists/lyrics in a marshaled Subsonic JSON envelope for the
// language. Objects are recognized by their shape: a "title" key marks a song, "albumCount" an
// artist, otherwise a "name" key marks an album. The walk is generic, so every endpoint that
// embeds songs/albums/artists is covered. The (already marshaled) input is returned unchanged when
// there is nothing to do.
func localizeJSON(body []byte, lang string) []byte {
	view := localization.view(lang)
	if emptyView(view) {
		return body
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return body
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return body
	}
	changed := localizeWalk(root, view)
	if !changed {
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func emptyView(v *langView) bool {
	for _, byID := range v.entities {
		if len(byID) > 0 {
			return false
		}
	}
	return true
}

func localizeWalk(node any, view *langView) (changed bool) {
	switch n := node.(type) {
	case map[string]any:
		if localizeObject(n, view) {
			changed = true
		}
		for _, v := range n {
			if localizeWalk(v, view) {
				changed = true
			}
		}
	case []any:
		for _, v := range n {
			if localizeWalk(v, view) {
				changed = true
			}
		}
	}
	return changed
}

func localizeObject(m map[string]any, view *langView) bool {
	id, _ := m["id"].(string)
	if id == "" {
		return false
	}
	var kind string
	if _, isSong := m["title"]; isSong {
		kind = "songs"
	} else if _, isArtist := m["albumCount"]; isArtist {
		kind = "artists"
	} else if _, isAlbum := m["name"]; isAlbum {
		kind = "albums"
	} else {
		return false
	}
	fields, ok := view.entities[kind][id]
	if !ok {
		return false
	}
	changed := false
	for _, key := range []string{"title", "name", "artist", "lyrics"} {
		if value, ok := fields[key]; ok {
			if _, exists := m[key]; exists {
				m[key] = value
				changed = true
			}
		}
	}
	return changed
}

// ── Endpoints.

// ListLocalization answers the languages that exist, and the language the embedded tags are in
// when a default.json names it.
func (api *Router) ListLocalization(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	entries, err := os.ReadDir(localizationDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	langs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && validLang(e.Name()) {
			langs = append(langs, e.Name())
		}
	}
	sort.Strings(langs)
	answer := map[string]any{"langs": langs}
	if data, err := os.ReadFile(filepath.Join(localizationDir(), "default.json")); err == nil {
		var def struct {
			Lang string `json:"lang"`
		}
		if json.Unmarshal(data, &def) == nil && validLang(def.Lang) {
			answer["defaultLang"] = def.Lang
		}
	}
	return writeProfileJSON(w, r, answer)
}

// GetLocalization (admin) reads one entity's overrides: ?lang=&kind=&id= → {"values": {…}}.
func (api *Router) GetLocalization(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	p := req.Params(r)
	lang, err := p.String("lang")
	if err != nil {
		return nil, err
	}
	kind, err := p.String("kind")
	if err != nil {
		return nil, err
	}
	id, err := p.String("id")
	if err != nil {
		return nil, err
	}
	if !validLang(lang) || !entityKinds[kind] || !validID(id) {
		return nil, newError(responses.ErrorGeneric, "lang, kind (songs|albums|artists) and id are required")
	}
	fields := localizedFields{}
	if data, err := os.ReadFile(filepath.Join(localizationEntityDir(lang, kind), id+".json")); err == nil {
		_ = json.Unmarshal(data, &fields)
	}
	return writeProfileJSON(w, r, map[string]any{"values": fields})
}

// SaveLocalization (admin) upserts one entity's overrides for a language: {lang, kind, id, values}.
// Fields absent from values keep their current value; values {} deletes the file. Unknown fields
// are rejected so typos don't silently do nothing.
func (api *Router) SaveLocalization(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return nil, newError(responses.ErrorGeneric, "reading body: "+err.Error())
	}
	var doc struct {
		Lang   string            `json:"lang"`
		Kind   string            `json:"kind"`
		ID     string            `json:"id"`
		Values localizedFields   `json:"values"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, newError(responses.ErrorGeneric, "body must be {lang, kind, id, values}: "+err.Error())
	}
	if !validLang(doc.Lang) || !entityKinds[doc.Kind] || doc.ID == "" || strings.ContainsAny(doc.ID, `/\`) {
		return nil, newError(responses.ErrorGeneric, "lang, kind (songs|albums|artists) and id are required")
	}
	allowed := map[string]bool{"title": true, "name": true, "artist": true, "lyrics": true}
	for k := range doc.Values {
		if !allowed[k] {
			return nil, newError(responses.ErrorGeneric, "unknown field: "+k)
		}
	}

	path := filepath.Join(localizationEntityDir(doc.Lang, doc.Kind), doc.ID+".json")
	if len(doc.Values) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		localization.drop(doc.Lang)
		return nil, nil
	}
	current := localizedFields{}
	if old, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(old, &current)
	}
	for k, v := range doc.Values {
		if v == "" {
			delete(current, k)
		} else {
			current[k] = v
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	if len(current) == 0 {
		_ = os.Remove(path)
	} else {
		out, err := json.Marshal(current)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, out, 0600); err != nil {
			return nil, err
		}
	}
	localization.drop(doc.Lang)
	log.Info(r, "Localization saved", "lang", doc.Lang, "kind", doc.Kind, "id", doc.ID)
	return nil, nil
}

// writeJSONAtomic is kept for files replaced as a whole (links, types).
func writeJSONAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
