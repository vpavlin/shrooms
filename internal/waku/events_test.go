package waku

import (
	"bytes"
	"testing"
)

// The July pin writes a received payload as an array of byte values, v0.40
// as base64 (MESSAGE_EVENTS.md), and v0.39's delivery module as
// {"_bytes": …}. All three are the same message.
func TestAReceivedPayloadReadsInEveryShape(t *testing.T) {
	want := []byte{0xfb, 0xff, 'h', 'i'}
	for name, payload := range map[string]string{
		"byte array":   `[251,255,104,105]`,
		"base64":       `"+/9oaQ=="`,
		"url-safe raw": `"-_9oaQ"`,
		"_bytes":       `{"_bytes":"-_9oaQ=="}`,
	} {
		raw := `{"eventType":"message_received","messageHash":"0x01","message":{"payload":` + payload +
			`,"contentTopic":"/t/1/x/proto","meta":"","timestamp":5},"source":"live"}`
		msg, hash, ok := ParseMessage(raw)
		if !ok || hash != "0x01" || !bytes.Equal(msg.Payload, want) || msg.Meta != nil || msg.Timestamp != 5 {
			t.Errorf("%s: got %v %q %v", name, msg, hash, ok)
		}
	}
}
