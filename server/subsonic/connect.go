package subsonic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
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

// BowPlayer Connect relay, advertised as "bowConnect". A user's devices keep a "connect" event stream open; the active
// device (the one that plays) publishes its state with connectState and the others send it commands with
// connectCommand. The server keeps which device is active and the last state, in memory only. The newer playing
// device wins: the one it replaces is told to pause. Spec: protocol/connect.md in the BowPlayer repo.

const (
	connectBuffer       = 64 // a stream this far behind is closed; the client reconnects and gets the full picture
	connectPing         = 15 * time.Second
	connectWriteTimeout = connectPing + 5*time.Second
	connectMaxState     = 2 << 20
	connectMaxCommand   = 1 << 20
	connectMaxName      = 64
)

var connectDeviceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var errDeviceOffline = newError(responses.ErrorDataNotFound, "device is not connected")

type connectEvent struct {
	name string
	data []byte
}

type connectConn struct {
	device, name, platform string
	events                 chan connectEvent
	done                   chan struct{}
	closeOnce              sync.Once
}

func (c *connectConn) close() { c.closeOnce.Do(func() { close(c.done) }) }

// send never blocks: a stream that can't keep up is closed instead of holding the hub's lock.
func (c *connectConn) send(name string, data []byte) {
	select {
	case c.events <- connectEvent{name, data}:
	default:
		c.close()
	}
}

type connectUser struct {
	conns      map[string]*connectConn
	active     string
	last       []byte // data of the complete "state" event devices get on connecting
	lastDevice string
	lastQueue  json.RawMessage
}

type connectHub struct {
	mu    sync.Mutex
	users map[string]*connectUser
	now   func() time.Time
}

func newConnectHub() *connectHub {
	return &connectHub{users: map[string]*connectUser{}, now: time.Now}
}

func (h *connectHub) user(id string) *connectUser {
	u := h.users[id]
	if u == nil {
		u = &connectUser{conns: map[string]*connectConn{}}
		h.users[id] = u
	}
	return u
}

func (h *connectHub) subscribe(userID, device, name, platform string) *connectConn {
	c := &connectConn{device: device, name: name, platform: platform,
		events: make(chan connectEvent, connectBuffer), done: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	u := h.user(userID)
	if old := u.conns[device]; old != nil {
		old.close()
	}
	u.conns[device] = c
	c.send("hello", mustJSON(map[string]any{"device": device, "serverTime": h.now().UnixMilli()}))
	h.sendDevices(u)
	if u.last != nil {
		c.send("state", u.last)
	}
	return c
}

func (h *connectHub) unsubscribe(userID string, c *connectConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	u := h.users[userID]
	if u == nil || u.conns[c.device] != c {
		return // already replaced by a newer stream of the same device
	}
	delete(u.conns, c.device)
	if u.active == c.device {
		u.active = ""
	}
	h.sendDevices(u)
}

func (h *connectHub) sendDevices(u *connectUser) {
	type device struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	devices := make([]device, 0, len(u.conns))
	for _, c := range u.conns {
		devices = append(devices, device{c.device, c.name, c.platform})
	}
	slices.SortFunc(devices, func(a, b device) int { return strings.Compare(a.ID, b.ID) })
	data := mustJSON(map[string]any{"active": u.active, "devices": devices})
	for _, c := range u.conns {
		c.send("devices", data)
	}
}

// publish takes a device's state. The active device's state is kept and sent on; another device's becomes active
// when it plays, when none is active or when it claims, and is ignored otherwise. A state without "queue" keeps the
// queue its device posted last; devices already online get it as posted, devices coming online get it complete.
func (h *connectHub) publish(userID, device string, state []byte, claim bool) error {
	var fields map[string]json.RawMessage
	if err := unmarshalObject(state, &fields); err != nil {
		return newError(responses.ErrorGeneric, "invalid state: %s", err.Error())
	}
	var playing bool
	if raw, ok := fields["playing"]; ok {
		if err := json.Unmarshal(raw, &playing); err != nil {
			return newError(responses.ErrorGeneric, "invalid state: playing: %s", err.Error())
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	u := h.users[userID]
	if u == nil || u.conns[device] == nil {
		return errDeviceOffline
	}
	c := u.conns[device]
	if u.active != device && !playing && !claim && u.active != "" {
		return nil
	}
	queue, hasQueue := fields["queue"]
	if !hasQueue {
		if u.lastDevice != device || u.lastQueue == nil {
			return newError(responses.ErrorMissingParameter, "queue required")
		}
		queue = u.lastQueue
	}
	if u.active != device {
		if old := u.conns[u.active]; old != nil {
			old.send("command", mustJSON(map[string]any{"cmd": "pause", "reason": "taken", "from": ""}))
		}
		u.active = device
		h.sendDevices(u)
	}
	at := h.now().UnixMilli()
	posted := mustJSON(map[string]any{"device": device, "name": c.name, "at": at, "state": fields})
	fields["queue"] = queue
	u.last = mustJSON(map[string]any{"device": device, "name": c.name, "at": at, "state": fields})
	u.lastDevice, u.lastQueue = device, queue
	for _, other := range u.conns {
		if other != c {
			other.send("state", posted)
		}
	}
	return nil
}

// command sends a command to the target device, marked with the device it came from.
func (h *connectHub) command(userID, from, target string, body []byte) error {
	var cmd map[string]json.RawMessage
	if err := unmarshalObject(body, &cmd); err != nil {
		return newError(responses.ErrorGeneric, "invalid command: %s", err.Error())
	}
	var name string
	if err := json.Unmarshal(cmd["cmd"], &name); err != nil || name == "" {
		return newError(responses.ErrorMissingParameter, "command has no cmd")
	}
	cmd["from"] = mustJSON(from)
	data := mustJSON(cmd)

	h.mu.Lock()
	defer h.mu.Unlock()
	u := h.users[userID]
	if u == nil || u.conns[target] == nil {
		return errDeviceOffline
	}
	u.conns[target].send("command", data)
	return nil
}

func unmarshalObject(data []byte, v any) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("not a JSON object")
	}
	return json.Unmarshal(data, v)
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // only ever called with maps of plain values and valid raw JSON
	}
	return data
}

func connectDevice(r *http.Request, param string) (string, error) {
	device, err := req.Params(r).String(param)
	if err != nil {
		return "", err
	}
	if !connectDeviceID.MatchString(device) {
		return "", newError(responses.ErrorGeneric, "invalid %s", param)
	}
	return device, nil
}

func connectBody(r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, newError(responses.ErrorGeneric, "body too large")
	}
	return body, nil
}

func connectUserID(r *http.Request) string {
	user, _ := request.UserFrom(r.Context())
	return user.ID
}

// Connect is the event stream a device keeps open while it's online.
func (api *Router) Connect(w http.ResponseWriter, r *http.Request) (*responses.Subsonic, error) {
	device, err := connectDevice(r, "device")
	if err != nil {
		return nil, err
	}
	p := req.Params(r)
	name := p.StringOr("name", device)
	if utf8.RuneCountInString(name) > connectMaxName {
		return nil, newError(responses.ErrorGeneric, "name too long")
	}
	platform := p.StringOr("platform", "desktop")
	if platform != "desktop" && platform != "android" && platform != "web" {
		return nil, newError(responses.ErrorGeneric, "invalid platform")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no") // nginx would otherwise hold events back
	// The status goes out explicitly: chi's compressor passes a Flush before WriteHeader through without noting
	// that the header went, and then sends it a second time with the first event.
	w.WriteHeader(http.StatusOK)
	ctx := r.Context()
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		log.Error(ctx, "Connect needs a streaming response", err)
		return nil, nil
	}

	userID := connectUserID(r)
	c := api.connect.subscribe(userID, device, name, platform)
	defer api.connect.unsubscribe(userID, c)
	log.Debug(ctx, "Connect device online", "device", device, "name", name)

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
		case <-c.done:
			log.Debug(ctx, "Connect stream closed: replaced or too far behind", "device", device)
			return nil, nil
		case ev = <-c.events:
		case <-ping.C:
			ev = connectEvent{"ping", mustJSON(map[string]any{"serverTime": api.connect.now().UnixMilli()})}
		}
		if err := write(ev); err != nil {
			log.Debug(ctx, "Connect stream write failed", "device", device, err)
			return nil, nil
		}
	}
}

// ConnectState publishes the calling device's state.
func (api *Router) ConnectState(r *http.Request) (*responses.Subsonic, error) {
	device, err := connectDevice(r, "device")
	if err != nil {
		return nil, err
	}
	body, err := connectBody(r, connectMaxState)
	if err != nil {
		return nil, err
	}
	claim := req.Params(r).BoolOr("claim", false)
	if err := api.connect.publish(connectUserID(r), device, body, claim); err != nil {
		return nil, err
	}
	return newResponse(), nil
}

// ConnectCommand sends a command to another device of the same user.
func (api *Router) ConnectCommand(r *http.Request) (*responses.Subsonic, error) {
	device, err := connectDevice(r, "device")
	if err != nil {
		return nil, err
	}
	target, err := connectDevice(r, "target")
	if err != nil {
		return nil, err
	}
	body, err := connectBody(r, connectMaxCommand)
	if err != nil {
		return nil, err
	}
	if err := api.connect.command(connectUserID(r), device, target, body); err != nil {
		return nil, err
	}
	return newResponse(), nil
}
