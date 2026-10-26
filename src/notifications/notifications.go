package notifications

// Package: notifications
// License: GPL-3.0 or later
//
// Captures freedesktop desktop notifications from the user session bus for the
// XeNeon notifications widget (roadmap G5/N1).
//
// org.freedesktop.Notifications.Notify is a method call, not a signal, and the
// eavesdrop match keyword was removed in dbus 1.9: the only supported way to
// observe it is org.freedesktop.DBus.Monitoring.BecomeMonitor on a private
// session-bus connection (D-Bus spec: BecomeMonitor(in as rules, in u flags)).
//
// Privacy: notifications are kept in a bounded in-memory ring only. Nothing is
// persisted, nothing is written to disk and notification content is never
// logged - arrival is logged without any fields.

import (
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/logger"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	notifyInterface = "org.freedesktop.Notifications"
	notifyMember    = "Notify"
	becomeMonitor   = "org.freedesktop.DBus.Monitoring.BecomeMonitor"

	// matchRule routes Notify method calls to the monitor connection.
	matchRule = "type='method_call',interface='" + notifyInterface + "',member='" + notifyMember + "'"

	// maxNotifications is the ring buffer capacity; the oldest entries are dropped.
	maxNotifications = 8
	// notificationTTL is how long a captured notification stays readable.
	notificationTTL = time.Hour
	// dedupeWindow collapses identical (app, summary, body) notifications that
	// arrive within this window of each other.
	dedupeWindow = 2 * time.Second
	// eavesdropBuffer absorbs bursts; godbus discards messages that do not fit,
	// which is preferable to stalling the connection for a UI mirror.
	eavesdropBuffer = 64
)

// Urgency values carried in Notification.Urgency (freedesktop notification spec).
const (
	UrgencyLow      = 0
	UrgencyNormal   = 1
	UrgencyCritical = 2
)

// Notification is a captured desktop notification. Summary and body are
// user-visible content: never log them and never persist them to disk.
type Notification struct {
	App       string    `json:"app"`
	Summary   string    `json:"summary"`
	Body      string    `json:"body"`
	Urgency   int       `json:"urgency"`
	Timestamp time.Time `json:"timestamp"`
}

// dedupeKey identifies a notification for dedupe purposes. A struct is used
// instead of joined strings because D-Bus strings may contain any UTF-8
// character, including separators.
type dedupeKey struct {
	app     string
	summary string
	body    string
}

var (
	mutex          sync.RWMutex
	conn           *dbus.Conn
	captureEnabled bool // capture is opt-in: off until SetCapture(true) is called
	stopped        bool
	items          []Notification // newest first, capped at maxNotifications
	lastSeen       = make(map[dedupeKey]time.Time)
)

// Init connects to the user session bus and registers a monitor for
// org.freedesktop.Notifications.Notify method calls. The session bus is absent
// when the daemon runs as the system service: failure logs a warning and the
// package degrades gracefully (no capture) instead of failing the daemon,
// mirroring the media module. Capture itself stays off until SetCapture(true).
func Init() {
	if config.IsSystemService() {
		logger.Log(logger.Fields{}).Warn("Notification capture is not available while service is running in system context")
		return
	}

	mutex.Lock()
	if conn != nil {
		mutex.Unlock()
		logger.Log(logger.Fields{}).Warn("Notification monitor already initialized")
		return
	}
	mutex.Unlock()

	connect()
}

// SetCapture gates notification capture. It defaults to off and is enabled
// only while a Notifications widget is placed - widget placement is the
// opt-in. Disabling closes the monitor connection entirely, so the bus stops
// delivering notification content to the daemon at all; re-enabling connects
// again. Captured history is kept when capture is disabled (in memory, TTL
// bounded). Calls after Stop are ignored. Enabling when the connection could
// not be established logs a warning; call SetCapture(true) again to retry.
func SetCapture(enabled bool) {
	mutex.Lock()
	if stopped {
		mutex.Unlock()
		return
	}

	captureEnabled = enabled
	needsConnect := enabled && conn == nil
	c := conn
	if !enabled {
		conn = nil
	}
	mutex.Unlock()

	if needsConnect {
		connect()
		return
	}

	if !enabled && c != nil {
		// Closing the connection removes its match rules on the bus and closes
		// the eavesdrop channel, which ends the receive loop.
		_ = c.Close()
	}
}

// GetNotifications returns copies of the currently captured notifications,
// newest first. Entries older than notificationTTL are filtered out.
func GetNotifications() []Notification {
	mutex.RLock()
	defer mutex.RUnlock()

	now := time.Now()
	out := make([]Notification, 0, len(items))
	for _, n := range items {
		if now.Sub(n.Timestamp) < notificationTTL {
			out = append(out, n)
		}
	}
	return out
}

// Stop closes the notification monitor connection.
func Stop() {
	mutex.Lock()
	if stopped {
		mutex.Unlock()
		logger.Log(logger.Fields{}).Warn("Notification monitor already stopped")
		return
	}

	stopped = true
	captureEnabled = false

	c := conn
	conn = nil
	mutex.Unlock()

	if c != nil {
		_ = c.Close()
	}
}

// connect opens a private session-bus connection, converts it into a monitor
// for matchRule and starts the receive loop. The mutex is deliberately not
// held across the connection setup; of concurrent attempts, the first to
// install wins and the losers close their own connection.
func connect() {
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Warn("Failed to connect to user session bus")
		return
	}

	// BecomeMonitor(in as rules, in u flags): match rules first, flags unused
	// (must be 0). The reply is awaited before enabling eavesdropping below,
	// because with eavesdropping active godbus diverts every incoming message
	// - including replies to our own pending calls - into the raw channel.
	err = c.BusObject().Call(becomeMonitor, 0, []string{matchRule}, uint32(0)).Err
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Warn("Failed to activate notification monitoring")
		_ = c.Close()
		return
	}

	ch := make(chan *dbus.Message, eavesdropBuffer)
	// With eavesdropping enabled, godbus delivers every incoming message raw
	// and skips its own dispatch, so the monitor never answers the method
	// calls it observes. Set after BecomeMonitor; the microscopic window
	// between the monitor becoming active and this call could at worst
	// misroute one already-matched Notify into the normal dispatch path.
	c.Eavesdrop(ch)

	mutex.Lock()
	if conn != nil || stopped {
		// Another attempt won the race or the daemon is shutting down.
		mutex.Unlock()
		_ = c.Close()
		return
	}
	conn = c
	mutex.Unlock()

	go monitorLoop(c, ch)
}

// monitorLoop consumes the raw message stream until the connection closes:
// conn.Close() closes the eavesdrop channel, which ends the loop. The same
// happens on transport EOF, when godbus closes the connection itself.
func monitorLoop(c *dbus.Conn, ch <-chan *dbus.Message) {
	for msg := range ch {
		capture(msg)
	}

	// Drop the stale handle unless a newer connection already replaced it.
	mutex.Lock()
	if conn == c {
		conn = nil
	}
	mutex.Unlock()
}

// capture filters the raw stream down to Notify method calls and stores them.
func capture(msg *dbus.Message) {
	mutex.RLock()
	enabled := captureEnabled
	mutex.RUnlock()

	if !enabled {
		return
	}

	if msg.Type != dbus.TypeMethodCall {
		return
	}

	iface, _ := msg.Headers[dbus.FieldInterface].Value().(string)
	member, _ := msg.Headers[dbus.FieldMember].Value().(string)
	if iface != notifyInterface || member != notifyMember {
		return
	}

	// Notify tuple: (app_name, replaces_id, app_icon, summary, body, actions,
	// hints, expire_timeout). replaces_id, app_icon, actions and
	// expire_timeout are not needed by the widget; retention is the fixed
	// notificationTTL of the ring, not the sender's expire_timeout.
	if len(msg.Body) < 5 {
		logger.Log(logger.Fields{}).Warn("Notification monitor received a malformed Notify message")
		return
	}

	app, _ := msg.Body[0].(string)
	summary, _ := msg.Body[3].(string)
	body, _ := msg.Body[4].(string)

	var hints map[string]dbus.Variant
	if len(msg.Body) > 6 {
		hints, _ = msg.Body[6].(map[string]dbus.Variant)
	}

	store(Notification{
		App:       app,
		Summary:   summary,
		Body:      body,
		Urgency:   urgencyFromHint(hints),
		Timestamp: time.Now(),
	})
}

// urgencyFromHint reads the "urgency" hint. The spec type is BYTE (0 low,
// 1 normal, 2 critical) and GNOME and KDE both send it that way, but senders
// have been seen using wider integer types: accept any integer width. Out of
// range or unrecognized values fall back to normal urgency, the spec default
// when the hint is absent.
func urgencyFromHint(hints map[string]dbus.Variant) int {
	v, ok := hints["urgency"]
	if !ok {
		return UrgencyNormal
	}

	var value int64
	switch x := v.Value().(type) {
	case uint8:
		value = int64(x)
	case int8:
		value = int64(x)
	case uint16:
		value = int64(x)
	case int16:
		value = int64(x)
	case uint32:
		value = int64(x)
	case int32:
		value = int64(x)
	case int64:
		value = x
	case uint64:
		value = int64(x)
	case uint:
		value = int64(x)
	case int:
		value = int64(x)
	default:
		return UrgencyNormal
	}

	switch {
	case value < 0:
		return UrgencyNormal
	case value > int64(UrgencyCritical):
		return UrgencyCritical
	default:
		return int(value)
	}
}

// store inserts n into the ring buffer: dedupe identical notifications within
// dedupeWindow, drop entries older than notificationTTL and cap the ring at
// maxNotifications, newest first.
func store(n Notification) {
	mutex.Lock()
	defer mutex.Unlock()

	now := time.Now()
	key := dedupeKey{app: n.App, summary: n.Summary, body: n.Body}
	if seen, ok := lastSeen[key]; ok && now.Sub(seen) < dedupeWindow {
		// Repeated identical notification: refresh the window so a rapid
		// burst collapses into a single capture.
		lastSeen[key] = now
		return
	}
	lastSeen[key] = now
	for k, t := range lastSeen {
		if now.Sub(t) >= dedupeWindow {
			delete(lastSeen, k)
		}
	}

	kept := make([]Notification, 0, maxNotifications)
	kept = append(kept, n)
	for _, item := range items {
		if now.Sub(item.Timestamp) < notificationTTL {
			kept = append(kept, item)
		}
	}
	if len(kept) > maxNotifications {
		kept = kept[:maxNotifications]
	}
	items = kept

	logger.Log(logger.Fields{}).Info("Notification captured")
}
