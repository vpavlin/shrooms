package wg

import "golang.zx2c4.com/wireguard/tun"

// Divert wraps the device WireGuard writes into, handing the packets match
// picks to take instead of the device: on the phone, the clipboard's, for a
// userspace stack (ADR-083), because the app that would take them from the
// kernel is outside its own tunnel. take must copy what it keeps; the buffer
// is WireGuard's.
//
// Under SelfOnly, so only what is addressed to this device is ever looked at.
// Reads pass straight through: nothing is added to the ordinary path.
func Divert(t tun.Device, match func([]byte) bool, take func([]byte)) tun.Device {
	return &divertTun{Device: t, match: match, take: take}
}

type divertTun struct {
	tun.Device
	match func([]byte) bool
	take  func([]byte)
}

func (t *divertTun) Write(bufs [][]byte, offset int) (int, error) {
	kept := bufs[:0:0]
	for _, b := range bufs {
		if p := b[offset:]; t.match(p) {
			t.take(p)
		} else {
			kept = append(kept, b)
		}
	}
	if len(kept) > 0 {
		if _, err := t.Device.Write(kept, offset); err != nil {
			return 0, err
		}
	}
	return len(bufs), nil
}
