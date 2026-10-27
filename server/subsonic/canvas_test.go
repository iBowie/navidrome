package subsonic

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GetCanvas", func() {
	var router *Router
	var w *httptest.ResponseRecorder
	var lib string

	write := func(name, data string) {
		Expect(os.WriteFile(filepath.Join(lib, name), []byte(data), 0600)).To(Succeed())
	}

	BeforeEach(func() {
		lib = GinkgoT().TempDir()
		repo := &tests.MockMediaFileRepo{}
		repo.SetData(model.MediaFiles{
			{ID: "s1", LibraryPath: lib, Path: "01 - One.flac"},
			{ID: "s2", LibraryPath: lib, Path: "02 - Two.flac"},
		})
		router = New(&tests.MockDataStore{MockedMediaFile: repo}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		w = httptest.NewRecorder()
	})

	It("serves the song's own canvas", func() {
		write("01 - One.canvas.mp4", "song clip")
		write("canvas.mp4", "folder clip")

		_, err := router.GetCanvas(w, newGetRequest("id=s1"))

		Expect(err).ToNot(HaveOccurred())
		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Header().Get("Content-Type")).To(Equal("video/mp4"))
		Expect(w.Body.String()).To(Equal("song clip"))
	})

	It("falls back to the folder's canvas", func() {
		write("01 - One.canvas.mp4", "song clip")
		write("canvas.mp4", "folder clip")

		_, err := router.GetCanvas(w, newGetRequest("id=s2"))

		Expect(err).ToNot(HaveOccurred())
		Expect(w.Body.String()).To(Equal("folder clip"))
	})

	It("answers 404 when there is no canvas", func() {
		res, err := router.GetCanvas(w, newGetRequest("id=s1"))

		Expect(err).ToNot(HaveOccurred())
		Expect(res).To(BeNil())
		Expect(w.Code).To(Equal(http.StatusNotFound))
	})

	It("answers 404 for an unknown song", func() {
		_, err := router.GetCanvas(w, newGetRequest("id=nope"))

		Expect(err).ToNot(HaveOccurred())
		Expect(w.Code).To(Equal(http.StatusNotFound))
	})

	It("serves byte ranges", func() {
		write("01 - One.canvas.mp4", "0123456789")
		r := newGetRequest("id=s1")
		r.Header.Set("Range", "bytes=2-4")

		_, err := router.GetCanvas(w, r)

		Expect(err).ToNot(HaveOccurred())
		Expect(w.Code).To(Equal(http.StatusPartialContent))
		Expect(w.Body.String()).To(Equal("234"))
	})
})
