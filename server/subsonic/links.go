package subsonic

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
)

// bowLinks: links between songs — an original and its remake, battle/alternate versions, or a
// DMCA-sensitive song and its clean counterpart. One file per song, so editing and syncing scale:
//
//	links/<song id>.json  {"links": [<SongLink>]}  // every link touching the song, both directions
//
//	{"a": "<song id>", "b": "<song id>", "kind": "version|alternate|counterpart",
//	 "label": "optional display text", "context": ["<playlist or album id>"], "merge": true}
//
// "context" empty means the link shows everywhere; otherwise only on those playlist or album
// pages — the DMCA counterpart, for example, shows its link only in the game OST playlist.
// "merge" asks clients to render both songs on one line (b under a) where the link is visible.
// Clients read with getSongLinks (batched by ids); admins edit with saveSongLinks, which keeps the
// copies under both ends in sync. Advertised as "bowLinks".

const linksTTL = 30 * time.Second

type songLink struct {
	A       string   `json:"a"`
	B       string   `json:"b"`
	Kind    string   `json:"kind"`
	Label   string   `json:"label,omitempty"`
	Context []string `json:"context,omitempty"`
	Merge   bool     `json:"merge,omitempty"`
}

func linksDir() string {
	return filepath.Join(conf.Server.DataFolder.String(), "links")
}

func linksFile(id string) string {
	return filepath.Join(linksDir(), id+".json")
}

func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..")
}

// linksKey identifies one relation regardless of which end's file holds it.
func linksKey(l songLink) string {
	a, b := l.A, l.B
	if b < a {
		a, b = b, a
	}
	return a + "|" + b + "|" + l.Kind
}

// ── In-memory view, like localization's.

type linksView struct {
	loadedAt time.Time
	byID     map[string][]songLink
}

type linksStore struct {
	mu    sync.Mutex
	view_ *linksView
}

var songLinksStore linksStore

func (s *linksStore) load() *linksView {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.view_ != nil && time.Since(s.view_.loadedAt) < linksTTL {
		return s.view_
	}
	v := &linksView{byID: map[string][]songLink{}}
	entries, err := os.ReadDir(linksDir())
	if err == nil {
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok {
				continue
			}
			data, err := os.ReadFile(linksFile(id))
			if err != nil {
				continue
			}
			var doc struct {
				Links []songLink `json:"links"`
			}
			if json.Unmarshal(data, &doc) == nil && len(doc.Links) > 0 {
				v.byID[id] = doc.Links
			}
		}
	}
	s.view_ = v
	return v
}

func (s *linksStore) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view_ = nil
}

// ── Endpoints.

// GetSongLinks answers the links for each requested song id: {"links": {id: […]}}.
func (api *Router) GetSongLinks(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	ids := r.URL.Query()["ids"]
	if len(ids) == 0 {
		if csv, err := req.Params(r).String("ids"); err == nil {
			ids = strings.Split(csv, ",")
		}
	}
	if len(ids) > 500 {
		return nil, newError(responses.ErrorGeneric, "at most 500 ids per call")
	}
	view := songLinksStore.load()
	answer := map[string]any{"links": map[string][]songLink{}}
	links := answer["links"].(map[string][]songLink)
	for _, id := range ids {
		if !validID(id) {
			continue
		}
		if l, ok := view.byID[id]; ok {
			links[id] = l
		}
	}
	return writeProfileJSON(w, r, answer)
}

// SaveSongLinks (admin) replaces one song's link list ({id, links}) and mirrors every change to
// the other end's file, so either side of a relation finds it.
func (api *Router) SaveSongLinks(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		return nil, newError(responses.ErrorGeneric, "reading body: "+err.Error())
	}
	var doc struct {
		ID    string     `json:"id"`
		Links []songLink `json:"links"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, newError(responses.ErrorGeneric, "body must be {id, links}: "+err.Error())
	}
	if !validID(doc.ID) {
		return nil, newError(responses.ErrorGeneric, "id is required")
	}
	for _, l := range doc.Links {
		if !validID(l.A) || !validID(l.B) || l.Kind == "" {
			return nil, newError(responses.ErrorGeneric, "every link needs valid a, b and kind")
		}
	}

	// What the other ends' files currently say about this song (old state) and should say (new).
	view := songLinksStore.load()
	oldList := view.byID[doc.ID]

	// Remove from other ends every old link that is gone from the new list.
	gone := map[string]songLink{}
	for _, l := range oldList {
		gone[linksKey(l)] = l
	}
	newKeys := map[string]bool{}
	for _, l := range doc.Links {
		newKeys[linksKey(l)] = true
	}
	for key, l := range gone {
		if !newKeys[key] {
			other := l.A
			if other == doc.ID {
				other = l.B
			}
			if err := removeLinkFrom(other, doc.ID, l); err != nil {
				return nil, err
			}
		}
	}

	if len(doc.Links) == 0 {
		if err := os.Remove(linksFile(doc.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	} else {
		out, err := json.Marshal(map[string]any{"links": doc.Links})
		if err != nil {
			return nil, err
		}
		if err := writeJSONAtomic(linksFile(doc.ID), out); err != nil {
			return nil, err
		}
	}

	// Upsert this song's new links into the other ends' files.
	for _, l := range doc.Links {
		other := l.A
		if other == doc.ID {
			other = l.B
		}
		if other == doc.ID {
			continue // a self-link lives only in the one file
		}
		if err := upsertLink(other, doc.ID, l); err != nil {
			return nil, err
		}
	}

	songLinksStore.drop()
	log.Info(r, "Song links saved", "id", doc.ID, "links", len(doc.Links))
	return nil, nil
}

// sameLink compares two links field by field (songLink contains a slice, so == doesn't compile).
func sameLink(x, l songLink) bool {
	return x.A == l.A && x.B == l.B && x.Kind == l.Kind && x.Label == l.Label &&
		x.Merge == l.Merge && slices.Equal(x.Context, l.Context)
}

func removeLinkFrom(otherID, aboutID string, l songLink) error {
	// Only remove the exact link (same relation, same direction).
	var doc struct {
		Links []songLink `json:"links"`
	}
	if data, err := os.ReadFile(linksFile(otherID)); err != nil {
		return nil // no file: nothing to remove
	} else {
		_ = json.Unmarshal(data, &doc)
	}
	var next []songLink
	changed := false
	for _, x := range doc.Links {
		if sameLink(x, l) {
			changed = true
			continue
		}
		next = append(next, x)
	}
	if !changed {
		return nil
	}
	if len(next) == 0 {
		return os.Remove(linksFile(otherID))
	}
	out, err := json.Marshal(map[string]any{"links": next})
	if err != nil {
		return err
	}
	return writeJSONAtomic(linksFile(otherID), out)
}

func upsertLink(otherID, aboutID string, l songLink) error {
	var doc struct {
		Links []songLink `json:"links"`
	}
	if data, err := os.ReadFile(linksFile(otherID)); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	for i, x := range doc.Links {
		if linksKey(x) == linksKey(l) {
			doc.Links[i] = l
			out, err := json.Marshal(map[string]any{"links": doc.Links})
			if err != nil {
				return err
			}
			return writeJSONAtomic(linksFile(otherID), out)
		}
	}
	doc.Links = append(doc.Links, l)
	out, err := json.Marshal(map[string]any{"links": doc.Links})
	if err != nil {
		return err
	}
	return writeJSONAtomic(linksFile(otherID), out)
}

// LinkIDs lists songs that have links (admin editors browse by id).
func LinkIDs() []string {
	view := songLinksStore.load()
	ids := make([]string, 0, len(view.byID))
	for id := range view.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
