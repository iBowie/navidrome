package nativeapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/subsonic"
)

// Library data (bowLocale/bowLinks/bowTypes): editing UI for the data-volume files behind the
// extensions the BowPlayer clients read. Admin only; the cores live in server/subsonic and are
// shared with the Subsonic endpoints, so both APIs edit the same files.

func (api *Router) addLibraryDataRoute(r chi.Router) {
	r.Route("/librarydata", func(r chi.Router) {
		r.Use(adminOnly)
		r.Get("/types", getTypes)
		r.Put("/types", putTypes)
		r.Get("/links", getLinks)
		r.Put("/links", putLinks)
		r.Get("/localization", getLocalization)
		r.Put("/localization", putLocalization)
	})
}

func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := request.UserFrom(r.Context())
		if !ok || !user.IsAdmin {
			http.Error(w, "Access denied: admin privileges required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error(r.Context(), "librarydata: encoding response", err)
	}
}

func getTypes(w http.ResponseWriter, r *http.Request) {
	playlists, albums, err := subsonic.ReadTypes()
	if err != nil {
		http.Error(w, "types.json is not valid: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"playlists": playlists, "albums": albums})
}

func putTypes(w http.ResponseWriter, r *http.Request) {
	var doc map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&doc); err != nil {
		http.Error(w, "body must be a JSON object: "+err.Error(), http.StatusBadRequest)
		return
	}
	data, _ := json.Marshal(doc)
	if err := subsonic.WriteTypes(data); err != nil {
		http.Error(w, "body must be a types document: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
}

func getLinks(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query()["ids"]
	writeJSON(w, r, http.StatusOK, map[string]any{"links": subsonic.ReadSongLinks(ids)})
}

func putLinks(w http.ResponseWriter, r *http.Request) {
	var doc struct {
		ID    string              `json:"id"`
		Links []subsonic.SongLink `json:"links"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&doc); err != nil {
		http.Error(w, "body must be {id, links}: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := subsonic.WriteSongLinks(doc.ID, doc.Links); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
}

func getLocalization(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	values, err := subsonic.ReadLocalization(q.Get("lang"), q.Get("kind"), q.Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"values": values})
}

func putLocalization(w http.ResponseWriter, r *http.Request) {
	var doc struct {
		Lang   string            `json:"lang"`
		Kind   string            `json:"kind"`
		ID     string            `json:"id"`
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&doc); err != nil {
		http.Error(w, "body must be {lang, kind, id, values}: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := subsonic.WriteLocalization(doc.Lang, doc.Kind, doc.ID, doc.Values); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
}
