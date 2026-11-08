package subsonic

import (
	"encoding/json"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Rooms", func() {
	var hub *roomHub

	roomNext := func(m *roomMember) (string, map[string]any) {
		var ev connectEvent
		ExpectWithOffset(1, m.events).To(Receive(&ev))
		var data map[string]any
		ExpectWithOffset(1, json.Unmarshal(ev.data, &data)).To(Succeed())
		return ev.name, data
	}
	roomDrain := func(ms ...*roomMember) {
		for _, m := range ms {
			for len(m.events) > 0 {
				<-m.events
			}
		}
	}
	roomErrCode := func(err error) int32 {
		var subErr subError
		ExpectWithOffset(1, errors.As(err, &subErr)).To(BeTrue())
		return subErr.code
	}

	newHub := func() *roomHub {
		h := newRoomHub()
		h.now = func() time.Time { return time.UnixMilli(1000) }
		return h
	}

	BeforeEach(func() { hub = newHub() })

	Describe("create", func() {
		It("creates a dj room with a 6-char code", func() {
			code, err := hub.create(roomTypeDJ)
			Expect(err).ToNot(HaveOccurred())
			Expect(code).To(HaveLen(6))
		})
		It("creates rooms with distinct codes", func() {
			a, _ := hub.create(roomTypeListen)
			b, _ := hub.create(roomTypeListen)
			Expect(a).ToNot(Equal(b))
		})
	})

	Describe("join", func() {
		It("makes the first member the host and sends hello", func() {
			code, _ := hub.create(roomTypeDJ)
			r, m, err := hub.join(code, "u1", "alice", "d1", "PC", "desktop")
			Expect(err).ToNot(HaveOccurred())
			Expect(r.host).To(Equal("u1/d1"))
			name, data := roomNext(m)
			Expect(name).To(Equal("hello"))
			Expect(data["code"]).To(Equal(code))
			Expect(data["type"]).To(Equal("dj"))
			Expect(data["host"]).To(Equal("u1/d1"))
		})

		It("rejects a 9th member", func() {
			code, _ := hub.create(roomTypeDJ)
			for i := 0; i < 8; i++ {
				_, _, err := hub.join(code, string(rune('a'+i)), "n", "d", "dev", "desktop")
				Expect(err).ToNot(HaveOccurred())
			}
			_, _, err := hub.join(code, "z", "n", "d", "dev", "desktop")
			Expect(err).To(HaveOccurred())
		})

		It("rejects an unknown code", func() {
			_, _, err := hub.join("XXXXXX", "u1", "a", "d", "dev", "desktop")
			Expect(err).To(HaveOccurred())
		})

		It("replaces a member's stale stream on reconnect", func() {
			code, _ := hub.create(roomTypeDJ)
			_, m1, _ := hub.join(code, "u1", "a", "d", "dev", "desktop")
			_, m2, _ := hub.join(code, "u1", "a", "d", "dev", "desktop")
			Expect(m1.done).To(BeClosed())
			hub.mu.Lock()
			r := hub.rooms[code]
			hub.mu.Unlock()
			Expect(r.members["u1/d"]).To(Equal(m2))
		})
	})

	Describe("state and commands", func() {
		It("fans a state out to the other members and keeps it for joiners", func() {
			code, _ := hub.create(roomTypeDJ)
			_, m1, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, m2, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(m1, m2)

			state := []byte(`{"queue":[{"key":1,"song":{"id":"s1"}}],"current":1,"position":0,"playing":true,"repeat":"off"}`)
			Expect(hub.publish(hub.rooms[code], m1, state)).To(Succeed())

			name, data := roomNext(m2)
			Expect(name).To(Equal("state"))
			st := data["state"].(map[string]any)
			Expect(st["current"]).To(Equal(float64(1)))
			Expect(st["queue"]).To(HaveLen(1))

			// a joiner gets the complete kept state
			_, m3, _ := hub.join(code, "u3", "c", "d3", "Web", "web")
			name, data = roomNext(m3) // hello
			Expect(name).To(Equal("hello"))
			name, data = roomNext(m3) // peers
			Expect(name).To(Equal("peers"))
			name, data = roomNext(m3)
			Expect(name).To(Equal("state"))
			Expect(data["state"].(map[string]any)["queue"]).To(HaveLen(1))
		})

		It("keeps the queue when a state omits it: the delta goes out without queue, the hub's kept state keeps it", func() {
			code, _ := hub.create(roomTypeDJ)
			r := hub.rooms[code]
			_, m1, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, m2, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(m1, m2)
			Expect(hub.publish(r, m1, []byte(`{"queue":[{"key":1}],"current":1,"position":0,"playing":true}`))).To(Succeed())
			roomDrain(m1, m2)
			Expect(hub.publish(r, m1, []byte(`{"current":1,"position":10,"playing":true}`))).To(Succeed())
			_, data := roomNext(m2)
			Expect(data["state"].(map[string]any)).ToNot(HaveKey("queue"))
			var kept map[string]any
			Expect(json.Unmarshal(r.last, &kept)).To(Succeed())
			Expect(kept["state"].(map[string]any)["queue"]).To(HaveLen(1))
		})

		It("relays a command to the other members with the sender", func() {
			code, _ := hub.create(roomTypeDJ)
			r := hub.rooms[code]
			_, m1, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, m2, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(m1, m2)
			Expect(hub.command(r, m1, []byte(`{"cmd":"next"}`))).To(Succeed())
			name, data := roomNext(m2)
			Expect(name).To(Equal("command"))
			Expect(data["cmd"]).To(Equal("next"))
			Expect(data["from"].(map[string]any)["key"]).To(Equal("u1/d1"))
		})

		It("rejects volume and transfer in rooms", func() {
			code, _ := hub.create(roomTypeDJ)
			r := hub.rooms[code]
			_, m1, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			Expect(roomErrCode(hub.command(r, m1, []byte(`{"cmd":"volume","value":0.5}`)))).To(Equal(int32(0)))
		})
	})

	Describe("listen rooms", func() {
		It("rejects non-host commands and state", func() {
			code, _ := hub.create(roomTypeListen)
			r := hub.rooms[code]
			_, host, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, guest, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(host, guest)
			Expect(hub.command(r, guest, []byte(`{"cmd":"next"}`))).To(HaveOccurred())
			Expect(hub.publish(r, guest, []byte(`{"queue":[],"current":null,"playing":false}`))).To(HaveOccurred())
			Expect(hub.command(r, host, []byte(`{"cmd":"next"}`))).To(Succeed())
		})

		It("relays a guest suggestion to the host and accepts it", func() {
			code, _ := hub.create(roomTypeListen)
			r := hub.rooms[code]
			_, host, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, guest, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(host, guest)

			id, err := hub.suggest(r, guest, []byte(`{"id":"s1","title":"Song"}`))
			Expect(err).ToNot(HaveOccurred())
			name, data := roomNext(host)
			Expect(name).To(Equal("suggestions"))
			suggestions := data["suggestions"].([]any)
			Expect(suggestions).To(HaveLen(1))
			Expect(suggestions[0].(map[string]any)["id"]).To(Equal(float64(id)))

			s, err := hub.suggestAct(r, host, id, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(s.From).To(Equal("u2/d2"))
			// host list is emptied and the guest is told
			_, data = roomNext(host)
			Expect(data["suggestions"]).To(BeEmpty())
			name, _ = roomNext(guest)
			Expect(name).To(Equal("suggestAccepted"))
		})

		It("rejects suggestions from the host, in dj rooms, and dismissals from guests", func() {
			code, _ := hub.create(roomTypeListen)
			r := hub.rooms[code]
			_, host, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, guest, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(host, guest)
			_, err := hub.suggest(r, host, []byte(`{"id":"s"}`))
			Expect(err).To(HaveOccurred())
			_, err = hub.suggestAct(r, guest, 1, false)
			Expect(err).To(HaveOccurred())

			djCode, _ := hub.create(roomTypeDJ)
			dj := hub.rooms[djCode]
			_, m, _ := hub.join(djCode, "u1", "a", "d1", "PC", "desktop")
			roomDrain(m)
			_, err = hub.suggest(dj, m, []byte(`{"id":"s"}`))
			Expect(err).To(HaveOccurred())
		})

		It("drops the oldest suggestion past the cap", func() {
			code, _ := hub.create(roomTypeListen)
			r := hub.rooms[code]
			_, host, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, guest, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(host, guest)
			for i := 0; i < 25; i++ {
				_, err := hub.suggest(r, guest, []byte(`{"id":"s"}`))
				Expect(err).ToNot(HaveOccurred())
			}
			Expect(r.suggestions).To(HaveLen(20))
			Expect(r.suggestions[0].ID).To(Equal(r.nextSuggest - 19))
		})
	})

	Describe("leaving", func() {
		It("migrates the host to the longest-joined member", func() {
			code, _ := hub.create(roomTypeDJ)
			r := hub.rooms[code]
			_, m1, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			hub.now = func() time.Time { return time.UnixMilli(2000) }
			_, m2, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			_, m3, _ := hub.join(code, "u3", "c", "d3", "Web", "web")
			roomDrain(m1, m2, m3)

			hub.leave(r, m1)
			Expect(hub.rooms[code]).ToNot(BeNil())
			Expect(r.host).To(Equal("u2/d2"))
			name, data := roomNext(m2)
			Expect(name).To(Equal("peers"))
			Expect(data["host"]).To(Equal("u2/d2"))
			name, data = roomNext(m2)
			Expect(name).To(Equal("host"))
			Expect(data["host"]).To(Equal("u2/d2"))
			name, data = roomNext(m3)
			Expect(name).To(Equal("peers"))
			name, data = roomNext(m3)
			Expect(name).To(Equal("host"))
			Expect(data["host"]).To(Equal("u2/d2"))
		})

		It("destroys the room when the last member leaves", func() {
			code, _ := hub.create(roomTypeDJ)
			r := hub.rooms[code]
			_, m1, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			hub.leave(r, m1)
			Expect(hub.rooms[code]).To(BeNil())
		})

		It("expires a room after 24h", func() {
			code, _ := hub.create(roomTypeDJ)
			hub.now = func() time.Time { return time.UnixMilli(1000).Add(25 * time.Hour) }
			_, _, err := hub.join(code, "u1", "a", "d", "dev", "desktop")
			Expect(err).To(HaveOccurred())
			Expect(hub.rooms[code]).To(BeNil())
		})
	})

	Describe("pos", func() {
		It("relays a position ping to the others", func() {
			code, _ := hub.create(roomTypeDJ)
			r := hub.rooms[code]
			_, m1, _ := hub.join(code, "u1", "a", "d1", "PC", "desktop")
			_, m2, _ := hub.join(code, "u2", "b", "d2", "Phone", "android")
			roomDrain(m1, m2)
			Expect(hub.pos(r, m1, []byte(`{"position":12.5}`))).To(Succeed())
			name, data := roomNext(m2)
			Expect(name).To(Equal("pos"))
			Expect(data["position"]).To(Equal(12.5))
			Expect(data["from"]).To(Equal("u1/d1"))
		})
	})
})
