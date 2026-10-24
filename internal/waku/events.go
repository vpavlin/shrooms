package waku

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Event types emitted by the packaged liblogosdelivery build.
const (
	EventMessageReceived  = "message_received"
	EventConnectionChange = "connection_change"
	EventConnectionStatus = "connection_status_change"
	EventRelayTopicHealth = "relay_topic_health_change"
)

// Message is a received Waku message.
//
// NOTE the asymmetry, verified against the packaged build: logosdelivery_send
// takes the payload as a base64 *string*, but message_received delivers it as a
// JSON *array of byte values*. Do not assume the encodings match. v0.40 sends
// base64 both ways (MESSAGE_EVENTS.md); see bytesField.
type Message struct {
	Payload      []byte `json:"payload"`
	ContentTopic string `json:"contentTopic"`
	Meta         []byte `json:"meta"`
	Version      int    `json:"version"`
	Timestamp    int64  `json:"timestamp"` // unix nanoseconds
	Ephemeral    bool   `json:"ephemeral"`
}

// messageEvent mirrors the message_received payload. Payload and meta are
// read by bytesField, because the two libraries write them differently.
type messageEvent struct {
	EventType   string `json:"eventType"`
	MessageHash string `json:"messageHash"`
	Message     struct {
		Payload      bytesField `json:"payload"`
		ContentTopic string     `json:"contentTopic"`
		Meta         bytesField `json:"meta"`
		Version      int        `json:"version"`
		Timestamp    int64      `json:"timestamp"`
		Ephemeral    bool       `json:"ephemeral"`
	} `json:"message"`
}

// EventType returns the eventType field of a raw event, or "" if absent.
func EventType(raw string) string {
	var probe struct {
		EventType string `json:"eventType"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	return probe.EventType
}

// ParseMessage extracts a received message from a raw event. It returns
// ok=false for any event that is not a message_received.
func ParseMessage(raw string) (msg Message, hash string, ok bool) {
	var ev messageEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return Message{}, "", false
	}
	if ev.EventType != EventMessageReceived {
		return Message{}, "", false
	}

	msg = Message{
		Payload:      []byte(ev.Message.Payload),
		ContentTopic: ev.Message.ContentTopic,
		Meta:         []byte(ev.Message.Meta),
		Version:      ev.Message.Version,
		Timestamp:    ev.Message.Timestamp,
		Ephemeral:    ev.Message.Ephemeral,
	}
	return msg, ev.MessageHash, true
}

// bytesField is a byte string in any of the shapes the libraries write: the
// July pin's array of byte values, v0.40's base64 string (standard, by
// MESSAGE_EVENTS.md; URL-safe and unpadded accepted too), and the
// {"_bytes": base64} object Loam saw from v0.39's delivery module
// (jimmy-crib/vpavlin, 2026-10-24). An empty string or null is no bytes.
type bytesField []byte

func (b *bytesField) UnmarshalJSON(raw []byte) error {
	var ints []int
	if err := json.Unmarshal(raw, &ints); err == nil {
		out := make([]byte, len(ints))
		for i, v := range ints {
			out[i] = byte(v)
		}
		*b = out
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var wrapped struct {
			Bytes *string `json:"_bytes"`
		}
		if err2 := json.Unmarshal(raw, &wrapped); err2 != nil || wrapped.Bytes == nil {
			return fmt.Errorf("bytes: neither an array, a string nor {\"_bytes\"}: %.40s", raw)
		}
		s = *wrapped.Bytes
	}
	if s == "" {
		*b = nil
		return nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if out, err := enc.DecodeString(s); err == nil {
			*b = out
			return nil
		}
	}
	return fmt.Errorf("bytes: not base64: %.40q", s)
}
