package main

import (
	"encoding/json"
	"testing"
)

// "This daemon does not report what it announces" and "this node announces
// nothing" are opposite diagnoses, and the wire has to keep them apart.
//
// One means an old daemon and nothing is wrong; the other means no peer can
// dial this node at all. Conflating them sends somebody hunting a fault that is
// not there — which is exactly what the first draft of this did, reporting
// "nothing" on a healthy laptop because the running daemon predated the field.
func TestAnnouncedDistinguishesAbsentFromEmpty(t *testing.T) {
	// An older daemon: the key is not in the JSON at all.
	var old statusPayload
	if err := json.Unmarshal([]byte(`{"name":"laptop"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Announced != nil {
		t.Error("a payload without the field decoded as if it had one")
	}

	// A current daemon on a node with nothing to announce.
	var none statusPayload
	if err := json.Unmarshal([]byte(`{"name":"phone","announced":[]}`), &none); err != nil {
		t.Fatal(err)
	}
	if none.Announced == nil {
		t.Fatal("an explicit empty list decoded as absent")
	}
	if len(*none.Announced) != 0 {
		t.Errorf("expected no addresses, got %v", *none.Announced)
	}

	// And the ordinary case survives the round trip.
	addrs := []string{"192.168.1.4:51820"}
	body, err := json.Marshal(statusPayload{Announced: &addrs})
	if err != nil {
		t.Fatal(err)
	}
	var back statusPayload
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if back.Announced == nil || len(*back.Announced) != 1 || (*back.Announced)[0] != addrs[0] {
		t.Errorf("round trip gave %v", back.Announced)
	}
}
