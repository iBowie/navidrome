package subsonic

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
)

// BowPlayer Listen Together rooms, advertised as "bowRoom". Registered users share one queue: the
// room type "dj" lets every member control it, "listen" restricts control to the host (others only
// listen, but can suggest songs the host accepts or dismisses). Members keep a "roomJoin" event
// stream open; control members publish state with roomState and send commands with roomCommand,
// which the hub relays to the other members. The shared state model and the command table are the
// Connect model (protocol/connect.md) minus volume and transfer. Everything is in memory only.
// Spec: protocol/room.md in the BowPlayer repo.

const (
	roomMaxMembers     = 8
	roomMaxSuggestions = 20
	roomCodeAlphabet   = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // Crockford-ish, no I/L/O/0/1
	roomCodeLength     = 6
	roomTTL            = 24 * time.Hour
	roomMaxSong        = 1 << 16 // a suggested song object, in bytes
)

var errRoomNotFound = newError(responses.ErrorDataNotFound, "room not found")
var errRoomFull = newError(responses.ErrorGeneric, "room is full")

type roomType string

const (
	roomTypeDJ     roomType = "dj"
	roomTypeListen roomType = "listen"
)

type roomMember struct {
	key       string // userID + "/" + device, unique in the room
	userID    string
	userName  string
	device    string
	name      string
	platform  string
	joinedAt  time.Time
	events    chan connectEvent
	done      chan struct{}
	closeOnce sync.Once
}

func (m *roomMember) close() { m.closeOnce.Do(func() { close(m.done) }) }

func (m *roomMember) send(name string, data []byte) {
	select {
	case m.events <- connectEvent{name, data}:
	default:
		m.close() // a stream this far behind is closed; the client reconnects
	}
}

type roomSuggestion struct {
	ID   int             `json:"id"`
	From string          `json:"from"` // member key
	Name string          `json:"name"` // suggester's display name
	Song json.RawMessage `json:"song"`
}

type room struct {
	code        string
	typ         roomType
	host        string // member key
	members     map[string]*roomMember
	createdAt   time.Time
	last        []byte // complete "state" event a joiner gets
	lastQueue   json.RawMessage
	lastFrom    string
	suggestions []roomSuggestion
	nextSuggest int
}

type roomHub struct {
	mu    sync.Mutex
	rooms map[string]*room
	now   func() time.Time
}

func newRoomHub() *roomHub {
	return &roomHub{rooms: map[string]*room{}, now: time.Now}
}

func roomCode() (string, error) {
	buf := make([]byte, roomCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = roomCodeAlphabet[int(b)%len(roomCodeAlphabet)]
	}
	return string(buf), nil
}

// get returns a live room, dropping an expired one.
func (h *roomHub) get(code string) *room {
	r := h.rooms[code]
	if r == nil {
		return nil
	}
	if h.now().Sub(r.createdAt) > roomTTL {
		delete(h.rooms, code)
		for _, m := range r.members {
			m.send("roomClosed", mustJSON(map[string]any{"reason": "expired"}))
			m.close()
		}
		return nil
	}
	return r
}

func (h *roomHub) create(typ roomType) (string, error) {
	code, err := roomCode()
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.get(code) != nil { // inconceivable with a random code; retry once rather than overwrite
		code, err = roomCode()
		if err != nil {
			return "", err
		}
	}
	h.rooms[code] = &room{code: code, typ: typ, members: map[string]*roomMember{}, createdAt: h.now(),
		suggestions: []roomSuggestion{}}
	return code, nil
}

// member describes a member for "peers" and "host" events.
func roomMemberJSON(m *roomMember) map[string]any {
	return map[string]any{"key": m.key, "userId": m.userID, "name": m.name, "device": m.device, "platform": m.platform}
}

// broadcast sends an event to every member except exclude (nil = all).
func broadcast(r *room, exclude *roomMember, name string, data []byte) {
	for _, m := range r.members {
		if m != exclude {
			m.send(name, data)
		}
	}
}

// join adds a member and returns the stream's events. A reconnect of the same key replaces the old
// stream (the client dropped and came back).
func (h *roomHub) join(code, userID, userName, device, name, platform string) (*room, *roomMember, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.get(code)
	if r == nil {
		return nil, nil, errRoomNotFound
	}
	key := userID + "/" + device
	if r.members[key] == nil && len(r.members) >= roomMaxMembers {
		return nil, nil, errRoomFull
	}
	m := &roomMember{key: key, userID: userID, userName: userName, device: device, name: name,
		platform: platform, joinedAt: h.now(),
		events: make(chan connectEvent, connectBuffer), done: make(chan struct{})}
	if old := r.members[key]; old != nil {
		old.close()
	}
	r.members[key] = m
	if r.host == "" {
		r.host = key
	}
	peers := make([]map[string]any, 0, len(r.members))
	for _, o := range r.members {
		peers = append(peers, roomMemberJSON(o))
	}
	slices.SortFunc(peers, func(a, b map[string]any) int {
		return strings.Compare(a["key"].(string), b["key"].(string))
	})
	hello := mustJSON(map[string]any{"code": code, "type": r.typ, "host": r.host, "you": key, "serverTime": h.now().UnixMilli()})
	m.send("hello", hello)
	m.send("peers", mustJSON(map[string]any{"host": r.host, "peers": peers}))
	broadcast(r, m, "peers", mustJSON(map[string]any{"host": r.host, "peers": peers}))
	if r.last != nil {
		m.send("state", r.last)
	}
	if r.typ == roomTypeListen && r.host == key {
		m.send("suggestions", mustJSON(map[string]any{"suggestions": r.suggestions}))
	}
	return r, m, nil
}

// leave removes a member; the host migrates to the longest-joined remaining member. The last
// member leaving (or an expired room) destroys it.
func (h *roomHub) leave(r *room, m *roomMember) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.members[m.key] != m {
		return // already replaced by a newer stream of the same member
	}
	delete(r.members, m.key)
	m.close()
	if len(r.members) == 0 {
		delete(h.rooms, r.code)
		return
	}
	if r.host == m.key {
		var next *roomMember
		for _, o := range r.members {
			if next == nil || o.joinedAt.Before(next.joinedAt) {
				next = o
			}
		}
		r.host = next.key
	}
	peers := make([]map[string]any, 0, len(r.members))
	for _, o := range r.members {
		peers = append(peers, roomMemberJSON(o))
	}
	broadcast(r, nil, "peers", mustJSON(map[string]any{"host": r.host, "peers": peers}))
	broadcast(r, nil, "host", mustJSON(map[string]any{"host": r.host}))
}

// canControl reports whether a member may drive the room.
func (r *room) canControl(m *roomMember) bool {
	return r.typ == roomTypeDJ || r.host == m.key
}

// publish stores a member's state and fans it out to the others. Like the Connect relay, a state
// without "queue" keeps the queue its poster last sent.
func (h *roomHub) publish(r *room, m *roomMember, state []byte) error {
	var fields map[string]json.RawMessage
	if err := unmarshalObject(state, &fields); err != nil {
		return newError(responses.ErrorGeneric, "invalid state: %s", err.Error())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.members[m.key] != m {
		return errRoomNotFound
	}
	queue, hasQueue := fields["queue"]
	if !hasQueue {
		if r.lastFrom != m.key || r.lastQueue == nil {
			return newError(responses.ErrorMissingParameter, "queue required")
		}
		queue = r.lastQueue
	}
	if !r.canControl(m) {
		return newError(responses.ErrorGeneric, "only the host controls this room")
	}
	at := h.now().UnixMilli()
	posted := mustJSON(map[string]any{"from": roomMemberJSON(m), "at": at, "state": fields})
	fields["queue"] = queue
	r.last = mustJSON(map[string]any{"from": roomMemberJSON(m), "at": at, "state": fields})
	r.lastFrom, r.lastQueue = m.key, queue
	broadcast(r, m, "state", posted)
	return nil
}

// pos relays a lightweight position ping, only meaningfully used while playing for drift correction.
func (h *roomHub) pos(r *room, m *roomMember, body []byte) error {
	var fields map[string]json.RawMessage
	if err := unmarshalObject(body, &fields); err != nil {
		return newError(responses.ErrorGeneric, "invalid pos: %s", err.Error())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.members[m.key] != m {
		return errRoomNotFound
	}
	fields["from"] = mustJSON(m.key)
	broadcast(r, m, "pos", mustJSON(fields))
	return nil
}

// command relays a queue/transport command. In a "listen" room only the host's commands relay.
func (h *roomHub) command(r *room, m *roomMember, body []byte) error {
	var cmd map[string]json.RawMessage
	if err := unmarshalObject(body, &cmd); err != nil {
		return newError(responses.ErrorGeneric, "invalid command: %s", err.Error())
	}
	var name string
	if err := json.Unmarshal(cmd["cmd"], &name); err != nil || name == "" {
		return newError(responses.ErrorMissingParameter, "command has no cmd")
	}
	if name == "volume" || name == "transfer" {
		return newError(responses.ErrorGeneric, "command not allowed in a room")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.members[m.key] != m {
		return errRoomNotFound
	}
	if !r.canControl(m) {
		return newError(responses.ErrorGeneric, "only the host controls this room")
	}
	cmd["from"] = mustJSON(roomMemberJSON(m))
	broadcast(r, m, "command", mustJSON(cmd))
	return nil
}

// suggest adds a song suggestion from a non-host member of a "listen" room and pushes the list to
// the host. Guests see their own pending suggestion acknowledged by the returned copy.
func (h *roomHub) suggest(r *room, m *roomMember, song []byte) (int, error) {
	if r.typ != roomTypeListen {
		return 0, newError(responses.ErrorGeneric, "suggestions are only for listen rooms")
	}
	if r.host == m.key {
		return 0, newError(responses.ErrorGeneric, "the host queues directly")
	}
	var obj map[string]json.RawMessage
	if err := unmarshalObject(song, &obj); err != nil {
		return 0, newError(responses.ErrorGeneric, "invalid song: %s", err.Error())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.members[m.key] != m {
		return 0, errRoomNotFound
	}
	if len(r.suggestions) >= roomMaxSuggestions {
		r.suggestions = r.suggestions[1:]
	}
	r.nextSuggest++
	s := roomSuggestion{ID: r.nextSuggest, From: m.key, Name: m.name, Song: song}
	r.suggestions = append(r.suggestions, s)
	if host := r.members[r.host]; host != nil {
		host.send("suggestions", mustJSON(map[string]any{"suggestions": r.suggestions}))
	}
	return s.ID, nil
}

// suggestAct applies the host's accept/dismiss of a suggestion and tells the suggester.
func (h *roomHub) suggestAct(r *room, m *roomMember, id int, accept bool) (*roomSuggestion, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.members[m.key] != m {
		return nil, errRoomNotFound
	}
	if r.host != m.key {
		return nil, newError(responses.ErrorGeneric, "only the host manages suggestions")
	}
	for i, s := range r.suggestions {
		if s.ID != id {
			continue
		}
		r.suggestions = slices.Delete(r.suggestions, i, i+1)
		if host := r.members[r.host]; host != nil {
			host.send("suggestions", mustJSON(map[string]any{"suggestions": r.suggestions}))
		}
		if from := r.members[s.From]; from != nil {
			if accept {
				from.send("suggestAccepted", mustJSON(map[string]any{"id": s.ID}))
			} else {
				from.send("suggestDismissed", mustJSON(map[string]any{"id": s.ID}))
			}
		}
		return &s, nil
	}
	return nil, newError(responses.ErrorDataNotFound, "no such suggestion")
}

// roomAndMember resolves the code param and the caller's membership.
func (h *roomHub) roomAndMember(r *http.Request) (*room, *roomMember, error) {
	code := req.Params(r).StringOr("code", "")
	h.mu.Lock()
	room := h.get(code)
	h.mu.Unlock()
	if room == nil {
		return nil, nil, errRoomNotFound
	}
	user, _ := request.UserFrom(r.Context())
	key := user.ID + "/" + req.Params(r).StringOr("device", "")
	h.mu.Lock()
	m := room.members[key]
	h.mu.Unlock()
	if m == nil {
		return nil, nil, errRoomNotFound
	}
	return room, m, nil
}

// RoomCreate creates a listen-together room and returns its invite code.
func (api *Router) RoomCreate(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	typ := roomType(req.Params(r).StringOr("type", string(roomTypeDJ)))
	if typ != roomTypeDJ && typ != roomTypeListen {
		return nil, newError(responses.ErrorGeneric, "type must be dj or listen")
	}
	code, err := api.rooms.create(typ)
	if err != nil {
		return nil, err
	}
	return writeProfileJSON(w, r, map[string]any{"code": code, "type": typ})
}

// RoomJoin is the event stream a member keeps open while in the room.
func (api *Router) RoomJoin(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	code := req.Params(r).StringOr("code", "")
	p := req.Params(r)
	device, err := connectDevice(r, "device")
	if err != nil {
		return nil, err
	}
	name := p.StringOr("name", device)
	if utf8.RuneCountInString(name) > connectMaxName {
		return nil, newError(responses.ErrorGeneric, "name too long")
	}
	platform := p.StringOr("platform", "desktop")
	if platform != "desktop" && platform != "android" && platform != "web" {
		return nil, newError(responses.ErrorGeneric, "invalid platform")
	}
	user, _ := request.UserFrom(r.Context())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	ctx := r.Context()
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		log.Error(ctx, "RoomJoin needs a streaming response", err)
		return nil, nil
	}

	room, m, err := api.rooms.join(code, user.ID, user.UserName, device, name, platform)
	if err != nil {
		return nil, err
	}
	defer api.rooms.leave(room, m)
	log.Debug(ctx, "Room member joined", "code", code, "member", m.key)

	ping := time.NewTicker(connectPing)
	defer ping.Stop()
	write := func(ev connectEvent) error {
		if err := rc.SetWriteDeadline(time.Now().Add(connectWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data); err != nil {
			return err
		}
		return rc.Flush()
	}
	for {
		var ev connectEvent
		select {
		case <-ctx.Done():
			return nil, nil
		case <-m.done:
			return nil, nil
		case ev = <-m.events:
		case <-ping.C:
			ev = connectEvent{"ping", mustJSON(map[string]any{"serverTime": api.rooms.now().UnixMilli()})}
		}
		if err := write(ev); err != nil {
			log.Debug(ctx, "Room stream write failed", "member", m.key, err)
			return nil, nil
		}
	}
}

// RoomLeave removes the caller from the room.
func (api *Router) RoomLeave(r *http.Request) (*responses.Subsonic, error) {
	room, m, err := api.rooms.roomAndMember(r)
	if err != nil {
		return nil, err
	}
	api.rooms.leave(room, m)
	return newResponse(), nil
}

// RoomState publishes the caller's state to the room. In a "listen" room only the host publishes.
func (api *Router) RoomState(r *http.Request) (*responses.Subsonic, error) {
	room, m, err := api.rooms.roomAndMember(r)
	if err != nil {
		return nil, err
	}
	body, err := connectBody(r, connectMaxState)
	if err != nil {
		return nil, err
	}
	if !room.canControl(m) {
		return nil, newError(responses.ErrorGeneric, "only the host controls this room")
	}
	if err := api.rooms.publish(room, m, body); err != nil {
		return nil, err
	}
	return newResponse(), nil
}

// RoomPos relays a position ping for drift correction.
func (api *Router) RoomPos(r *http.Request) (*responses.Subsonic, error) {
	room, m, err := api.rooms.roomAndMember(r)
	if err != nil {
		return nil, err
	}
	body, err := connectBody(r, 1<<10)
	if err != nil {
		return nil, err
	}
	if err := api.rooms.pos(room, m, body); err != nil {
		return nil, err
	}
	return newResponse(), nil
}

// RoomCommand relays a command to the room's other members.
func (api *Router) RoomCommand(r *http.Request) (*responses.Subsonic, error) {
	room, m, err := api.rooms.roomAndMember(r)
	if err != nil {
		return nil, err
	}
	body, err := connectBody(r, connectMaxCommand)
	if err != nil {
		return nil, err
	}
	if err := api.rooms.command(room, m, body); err != nil {
		return nil, err
	}
	return newResponse(), nil
}

// RoomSuggest adds a song suggestion ("listen" rooms, non-host members).
func (api *Router) RoomSuggest(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	room, m, err := api.rooms.roomAndMember(r)
	if err != nil {
		return nil, err
	}
	body, err := connectBody(r, roomMaxSong)
	if err != nil {
		return nil, err
	}
	id, err := api.rooms.suggest(room, m, body)
	if err != nil {
		return nil, err
	}
	return writeProfileJSON(w, r, map[string]any{"id": id})
}

// RoomSuggestAct accepts or dismisses a suggestion (host only).
func (api *Router) RoomSuggestAct(r *http.Request) (*responses.Subsonic, error) {
	room, m, err := api.rooms.roomAndMember(r)
	if err != nil {
		return nil, err
	}
	p := req.Params(r)
	id, err := p.Int64("id")
	if err != nil {
		return nil, err
	}
	if _, err := api.rooms.suggestAct(room, m, int(id), p.BoolOr("accept", false)); err != nil {
		return nil, err
	}
	return newResponse(), nil
}
