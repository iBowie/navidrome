package subsonic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connect", func() {
	var hub *connectHub

	// next returns the device's next event, decoding its data into a map.
	next := func(c *connectConn) (string, map[string]any) {
		var ev connectEvent
		ExpectWithOffset(1, c.events).To(Receive(&ev))
		var data map[string]any
		ExpectWithOffset(1, json.Unmarshal(ev.data, &data)).To(Succeed())
		return ev.name, data
	}
	// last skips to the device's latest queued event.
	last := func(c *connectConn) (string, map[string]any) {
		ExpectWithOffset(1, c.events).ToNot(BeEmpty())
		for len(c.events) > 1 {
			<-c.events
		}
		return next(c)
	}
	drain := func(cs ...*connectConn) {
		for _, c := range cs {
			for len(c.events) > 0 {
				<-c.events
			}
		}
	}
	errCode := func(err error) int32 {
		var subErr subError
		ExpectWithOffset(1, errors.As(err, &subErr)).To(BeTrue())
		return subErr.code
	}
	playing := []byte(`{"queue":[{"key":1,"id":"s1","next":false}],"current":1,"position":12.5,"playing":true}`)
	paused := []byte(`{"queue":[],"current":null,"position":0,"playing":false}`)

	BeforeEach(func() {
		hub = newConnectHub()
		hub.now = func() time.Time { return time.UnixMilli(1_000_000) }
	})

	Describe("streams", func() {
		It("says hello and lists the devices", func() {
			pc := hub.subscribe("u1", "pc", "Desk PC", "desktop")

			name, data := next(pc)
			Expect(name).To(Equal("hello"))
			Expect(data).To(Equal(map[string]any{"device": "pc", "serverTime": 1_000_000.0}))
			name, data = next(pc)
			Expect(name).To(Equal("devices"))
			Expect(data).To(Equal(map[string]any{"active": "", "devices": []any{
				map[string]any{"id": "pc", "name": "Desk PC", "platform": "desktop"},
			}}))
			Expect(pc.events).To(BeEmpty())
		})

		It("tells every device when another comes and goes", func() {
			pc := hub.subscribe("u1", "pc", "Desk PC", "desktop")
			drain(pc)

			phone := hub.subscribe("u1", "phone", "Galaxy", "android")
			name, data := last(pc)
			Expect(name).To(Equal("devices"))
			Expect(data["devices"]).To(HaveLen(2))
			_, data = last(phone)
			Expect(data["devices"]).To(HaveLen(2))

			hub.unsubscribe("u1", phone)
			_, data = last(pc)
			Expect(data["devices"]).To(Equal([]any{map[string]any{"id": "pc", "name": "Desk PC", "platform": "desktop"}}))
		})

		It("keeps users apart", func() {
			pc := hub.subscribe("u1", "pc", "Desk PC", "desktop")
			drain(pc)
			other := hub.subscribe("u2", "phone", "Their phone", "android")
			Expect(hub.publish("u2", "phone", playing)).To(Succeed())

			Expect(pc.events).To(BeEmpty())
			Expect(errCode(hub.command("u2", "phone", "pc", []byte(`{"cmd":"pause"}`)))).To(Equal(responses.ErrorDataNotFound))
			drain(other)
		})

		It("replaces a device's older stream", func() {
			old := hub.subscribe("u1", "pc", "Desk PC", "desktop")
			newer := hub.subscribe("u1", "pc", "Desk PC", "desktop")
			Expect(old.done).To(BeClosed())

			hub.unsubscribe("u1", old)
			_, data := last(newer)
			Expect(data["devices"]).To(HaveLen(1))
			Expect(hub.publish("u1", "pc", playing)).To(Succeed())
		})

		It("closes a stream that falls too far behind", func() {
			pc := hub.subscribe("u1", "pc", "Desk PC", "desktop")
			phone := hub.subscribe("u1", "phone", "Galaxy", "android")
			for range connectBuffer {
				Expect(hub.command("u1", "phone", "pc", []byte(`{"cmd":"next"}`))).To(Succeed())
			}

			Expect(pc.done).To(BeClosed())
			Expect(phone.done).ToNot(BeClosed())
		})
	})

	Describe("state", func() {
		var pc, phone *connectConn

		BeforeEach(func() {
			pc = hub.subscribe("u1", "pc", "Desk PC", "desktop")
			phone = hub.subscribe("u1", "phone", "Galaxy", "android")
			drain(pc, phone)
		})

		It("makes the playing device active and sends its state to the others", func() {
			Expect(hub.publish("u1", "pc", playing)).To(Succeed())

			name, data := next(phone)
			Expect(name).To(Equal("devices"))
			Expect(data["active"]).To(Equal("pc"))
			name, data = next(phone)
			Expect(name).To(Equal("state"))
			Expect(data["device"]).To(Equal("pc"))
			Expect(data["name"]).To(Equal("Desk PC"))
			Expect(data["at"]).To(Equal(1_000_000.0))
			Expect(data["state"]).To(HaveKeyWithValue("position", 12.5))

			name, _ = last(pc)
			Expect(name).To(Equal("devices"), "the publisher doesn't get its own state back")
		})

		It("pauses the old device when another starts playing", func() {
			Expect(hub.publish("u1", "pc", playing)).To(Succeed())
			drain(pc, phone)

			Expect(hub.publish("u1", "phone", playing)).To(Succeed())

			name, data := next(pc)
			Expect(name).To(Equal("command"))
			Expect(data).To(Equal(map[string]any{"cmd": "pause", "reason": "taken", "from": ""}))
			name, data = next(pc)
			Expect(name).To(Equal("devices"))
			Expect(data["active"]).To(Equal("phone"))
			name, data = next(pc)
			Expect(name).To(Equal("state"))
			Expect(data["device"]).To(Equal("phone"))
		})

		It("ignores a paused state from an idle device while another is active", func() {
			Expect(hub.publish("u1", "pc", playing)).To(Succeed())
			drain(pc, phone)

			Expect(hub.publish("u1", "phone", paused)).To(Succeed())

			Expect(pc.events).To(BeEmpty())
			Expect(hub.users["u1"].active).To(Equal("pc"))
		})

		It("lets a paused device become active when none is", func() {
			Expect(hub.publish("u1", "phone", paused)).To(Succeed())

			_, data := next(pc)
			Expect(data["active"]).To(Equal("phone"))
		})

		It("keeps the last state for devices coming online, after its device left", func() {
			Expect(hub.publish("u1", "pc", playing)).To(Succeed())
			hub.unsubscribe("u1", pc)
			_, data := last(phone)
			Expect(data["active"]).To(Equal(""))

			tablet := hub.subscribe("u1", "tablet", "Tab", "android")
			next(tablet) // hello
			next(tablet) // devices
			name, data := next(tablet)
			Expect(name).To(Equal("state"))
			Expect(data["device"]).To(Equal("pc"))
			Expect(data["state"]).To(HaveKeyWithValue("playing", true))
		})

		It("refuses a device without a stream", func() {
			Expect(errCode(hub.publish("u1", "tablet", playing))).To(Equal(responses.ErrorDataNotFound))
		})

		It("refuses anything but a JSON object", func() {
			for _, body := range []string{``, `null`, `[]`, `"x"`, `{"playing":"yes"}`, `{"playing":true`} {
				Expect(errCode(hub.publish("u1", "pc", []byte(body)))).To(Equal(responses.ErrorGeneric), body)
			}
		})
	})

	Describe("commands", func() {
		var pc, phone *connectConn

		BeforeEach(func() {
			pc = hub.subscribe("u1", "pc", "Desk PC", "desktop")
			phone = hub.subscribe("u1", "phone", "Galaxy", "android")
			drain(pc, phone)
		})

		It("sends a command to its target, marked with the sender", func() {
			Expect(hub.command("u1", "phone", "pc", []byte(`{"cmd":"seek","position":30,"from":"forged"}`))).To(Succeed())

			name, data := next(pc)
			Expect(name).To(Equal("command"))
			Expect(data).To(Equal(map[string]any{"cmd": "seek", "position": 30.0, "from": "phone"}))
			Expect(phone.events).To(BeEmpty())
		})

		It("refuses an offline target", func() {
			Expect(errCode(hub.command("u1", "phone", "tablet", []byte(`{"cmd":"play"}`)))).To(Equal(responses.ErrorDataNotFound))
		})

		It("refuses a command without cmd", func() {
			for _, body := range []string{`{}`, `{"cmd":""}`, `{"cmd":3}`} {
				Expect(errCode(hub.command("u1", "phone", "pc", []byte(body)))).To(Equal(responses.ErrorMissingParameter), body)
			}
			for _, body := range []string{`null`, `[1]`, `{`} {
				Expect(errCode(hub.command("u1", "phone", "pc", []byte(body)))).To(Equal(responses.ErrorGeneric), body)
			}
		})
	})

	Describe("endpoints", func() {
		var router *Router
		var srv *httptest.Server
		var cancel context.CancelFunc

		withUser := func(r *http.Request) *http.Request {
			return r.WithContext(request.WithUser(r.Context(), model.User{ID: "u1", UserName: "bow"}))
		}

		BeforeEach(func() {
			router = New(&tests.MockDataStore{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := router.Connect(w, withUser(r))
				if err != nil {
					sendError(w, r, err)
				}
			}))
			DeferCleanup(srv.Close)
		})

		open := func(query string) (*http.Response, *bufio.Reader) {
			var ctx context.Context
			ctx, cancel = context.WithCancel(context.Background())
			DeferCleanup(func() { cancel() })
			req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/rest/connect?"+query, nil)
			Expect(err).ToNot(HaveOccurred())
			res, err := http.DefaultClient.Do(req)
			Expect(err).ToNot(HaveOccurred())
			return res, bufio.NewReader(res.Body)
		}
		readEvent := func(r *bufio.Reader) (string, string) {
			var name, data string
			for {
				line, err := r.ReadString('\n')
				ExpectWithOffset(1, err).ToNot(HaveOccurred())
				line = strings.TrimSuffix(line, "\n")
				switch {
				case line == "":
					return name, data
				case strings.HasPrefix(line, "event: "):
					name = strings.TrimPrefix(line, "event: ")
				case strings.HasPrefix(line, "data: "):
					data = strings.TrimPrefix(line, "data: ")
				}
			}
		}
		post := func(path, query, body string) *http.Request {
			r := httptest.NewRequest("POST", "/rest/"+path+"?"+query, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			return withUser(r)
		}

		It("streams events and publishes state over HTTP", func() {
			res, events := open("device=pc&name=Desk+PC&platform=desktop")
			Expect(res.StatusCode).To(Equal(http.StatusOK))
			Expect(res.Header.Get("Content-Type")).To(Equal("text/event-stream"))
			Expect(res.Header.Get("X-Accel-Buffering")).To(Equal("no"))
			name, _ := readEvent(events)
			Expect(name).To(Equal("hello"))
			name, data := readEvent(events)
			Expect(name).To(Equal("devices"))
			Expect(data).To(ContainSubstring(`"name":"Desk PC"`))

			phone := router.connect.subscribe("u1", "phone", "Galaxy", "android")
			readEvent(events) // devices, now with the phone
			Expect(router.connect.publish("u1", "phone", playing)).To(Succeed())
			_, data = readEvent(events)
			Expect(data).To(ContainSubstring(`"active":"phone"`))
			name, data = readEvent(events)
			Expect(name).To(Equal("state"))
			Expect(data).To(ContainSubstring(`"device":"phone"`))

			res2, err := router.ConnectCommand(post("connectCommand", "device=pc&target=phone", `{"cmd":"pause"}`))
			Expect(err).ToNot(HaveOccurred())
			Expect(res2.Status).To(Equal(responses.StatusOK))
			_, cmd := last(phone)
			Expect(cmd).To(Equal(map[string]any{"cmd": "pause", "from": "pc"}))

			_, err = router.ConnectState(post("connectState", "device=pc", string(playing)))
			Expect(err).ToNot(HaveOccurred())
			_, cmd = last(phone)
			Expect(cmd).To(HaveKeyWithValue("device", "pc"))

			cancel()
			Eventually(func() int {
				router.connect.mu.Lock()
				defer router.connect.mu.Unlock()
				return len(router.connect.users["u1"].conns)
			}).Should(Equal(1), "the closed stream takes its device offline")
		})

		It("checks the device parameters", func() {
			for _, query := range []string{"", "device=a/b", "device=pc&platform=ios", "device=pc&name=" + strings.Repeat("x", 65)} {
				res, _ := open(query)
				Expect(res.Header.Get("Content-Type")).ToNot(Equal("text/event-stream"), query)
			}
			_, err := router.ConnectCommand(post("connectCommand", "device=pc", `{"cmd":"pause"}`))
			Expect(err).To(HaveOccurred())
		})

		It("refuses a body over the limit", func() {
			_, err := router.ConnectState(post("connectState", "device=pc", `{"x":"`+strings.Repeat("x", connectMaxState)+`"}`))
			Expect(err).To(MatchError(ContainSubstring("body too large")))
		})
	})
})
