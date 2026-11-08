package subsonic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("bowProfile", func() {
	var api *Router
	var userRepo *tests.MockedUserRepo
	var playlistRepo *tests.MockPlaylistRepo

	caller := model.User{ID: "caller", UserName: "carol"}

	profileReq := func(h func(http.ResponseWriter, *http.Request) (*responses.Subsonic, error), url string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r = r.WithContext(request.WithUser(GinkgoT().Context(), caller))
		w := httptest.NewRecorder()
		_, err := h(w, r)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		return w
	}

	BeforeEach(func() {
		userRepo = tests.CreateMockUserRepo()
		playlistRepo = tests.CreateMockPlaylistRepo()
		ds := &tests.MockDataStore{MockedUser: userRepo, MockedPlaylist: playlistRepo}
		api = &Router{ds: ds}
	})

	Describe("SearchUsers", func() {
		It("matches by user name and display name, case-insensitive", func() {
			userRepo.Data["1"] = &model.User{ID: "1", UserName: "Alice", Name: "Alice A"}
			userRepo.Data["2"] = &model.User{ID: "2", UserName: "bob", Name: "Robert"}
			userRepo.Data["3"] = &model.User{ID: "3", UserName: "carol"}
			w := profileReq(api.SearchUsers, "/searchUsers?query=ali")
			var body struct {
				Users []struct {
					ID       string `json:"id"`
					Name     string `json:"name"`
					UserName string `json:"userName"`
				} `json:"users"`
			}
			Expect(json.NewDecoder(w.Body).Decode(&body)).To(Succeed())
			Expect(body.Users).To(HaveLen(1))
			Expect(body.Users[0].UserName).To(Equal("Alice"))
		})

		It("lists everyone with an empty query", func() {
			userRepo.Data["1"] = &model.User{ID: "1", UserName: "alice"}
			userRepo.Data["2"] = &model.User{ID: "2", UserName: "bob"}
			w := profileReq(api.SearchUsers, "/searchUsers?query=")
			Expect(w.Body.String()).To(ContainSubstring(`"userName":"alice"`))
			Expect(w.Body.String()).To(ContainSubstring(`"userName":"bob"`))
		})

		It("includes an avatarUrl from the email", func() {
			userRepo.Data["1"] = &model.User{ID: "1", UserName: "alice", Email: "alice@example.com"}
			w := profileReq(api.SearchUsers, "/searchUsers?query=alice")
			Expect(w.Body.String()).To(ContainSubstring("gravatar.com/avatar/"))
		})
	})

	Describe("GetUserProfile", func() {
		It("resolves by id and by name", func() {
			userRepo.Data["1"] = &model.User{ID: "1", UserName: "alice", Name: "Alice A"}
			userRepo.Data["alice"] = userRepo.Data["1"] // FindByUsername keys by lower-cased name
			Expect(profileReq(api.GetUserProfile, "/getUserProfile?user=1").Body.String()).To(ContainSubstring(`"name":"Alice A"`))
			Expect(profileReq(api.GetUserProfile, "/getUserProfile?user=ALICE").Body.String()).To(ContainSubstring(`"id":"1"`))
		})

		It("returns not-found for an unknown user", func() {
			r := httptest.NewRequest("GET", "/getUserProfile?user=nobody", nil)
			r = r.WithContext(request.WithUser(GinkgoT().Context(), caller))
			_, err := api.GetUserProfile(httptest.NewRecorder(), r)
			Expect(err).To(Equal(errUserNotFound))
		})
	})

	Describe("GetUserPlaylists", func() {
		It("returns only the target's public playlists", func() {
			userRepo.Data["1"] = &model.User{ID: "1", UserName: "alice"}
			playlistRepo.All = model.Playlists{
				{ID: "p1", Name: "Public mix", OwnerID: "1", Public: true, SongCount: 3},
				{ID: "p2", Name: "Secret mix", OwnerID: "1", Public: false},
				{ID: "p3", Name: "Other user", OwnerID: "2", Public: true},
			}
			w := profileReq(api.GetUserPlaylists, "/getUserPlaylists?user=1")
			body := w.Body.String()
			Expect(body).To(ContainSubstring("Public mix"))
			Expect(body).ToNot(ContainSubstring("Secret mix"))
			Expect(body).ToNot(ContainSubstring("Other user"))
		})
	})

	Describe("profileUser", func() {
		It("returns errUserNotFound for a missing user", func() {
			r := httptest.NewRequest("GET", "/getUserProfile?user=nobody", nil)
			r = r.WithContext(request.WithUser(GinkgoT().Context(), caller))
			_, err := api.profileUser(r)
			Expect(err).To(Equal(errUserNotFound))
		})

		It("prefers the id match over the name", func() {
			userRepo.Data["zzz"] = &model.User{ID: "zzz", UserName: "alice"}
			userRepo.Data["alice"] = &model.User{ID: "alice", UserName: "alice2"}
			r := httptest.NewRequest("GET", "/getUserProfile?user=alice", nil)
			r = r.WithContext(request.WithUser(GinkgoT().Context(), caller))
			u, err := api.profileUser(r)
			Expect(err).ToNot(HaveOccurred())
			Expect(u.ID).To(Equal("alice"))
		})
	})
})
