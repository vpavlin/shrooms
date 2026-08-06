package waku

import "encoding/json"

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
// JSON *array of byte values*. Do not assume the encodings match.
type Message struct {
	Payload      []byte `json:"payload"`
	ContentTopic string `json:"contentTopic"`
	Meta         []byte `json:"meta"`
	Version      int    `json:"version"`
	Timestamp    int64  `json:"timestamp"` // unix nanoseconds
	Ephemeral    bool   `json:"ephemeral"`
}

// messageEvent mirrors the message_received payload. Payload is decoded via
// []int because the C layer emits a byte array, which encoding/json will not
// unmarshal into []byte (it expects base64 for that).
type messageEvent struct {
	EventType   string `json:"eventType"`
	MessageHash string `json:"messageHash"`
	Message     struct {
		Payload      []int  `json:"payload"`
		ContentTopic string `json:"contentTopic"`
		Meta         []int  `json:"meta"`
		Version      int    `json:"version"`
		Timestamp    int64  `json:"timestamp"`
		Ephemeral    bool   `json:"ephemeral"`
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
		Payload:      bytesFromInts(ev.Message.Payload),
		ContentTopic: ev.Message.ContentTopic,
		Meta:         bytesFromInts(ev.Message.Meta),
		Version:      ev.Message.Version,
		Timestamp:    ev.Message.Timestamp,
		Ephemeral:    ev.Message.Ephemeral,
	}
	return msg, ev.MessageHash, true
}

func bytesFromInts(in []int) []byte {
	if len(in) == 0 {
		return nil
	}
	out := make([]byte, len(in))
	for i, v := range in {
		out[i] = byte(v)
	}
	return out
}
