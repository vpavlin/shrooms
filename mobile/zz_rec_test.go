package mobile

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestCardRecovery(t *testing.T) {
	pub, _ := hex.DecodeString("04681f8e3090d5a25c8c0901f7b0cc4e658265e56885663fab6f8f7efd380fa1e15c32088f67de556ccaaf8fbe89ef6362a2522b067b156eb77c5d99d26ad16f95")
	r, _ := hex.DecodeString("40b1292b81ae4dc26e6b486ddeb88ef746736c46c00cc92e51ae16a4c9dd5ac5")
	s, _ := hex.DecodeString("038cafd15f9551721c546ae20291235fcf68b144c5afa456aab96c7ddf2f002f")
	dig, _ := hex.DecodeString("39939788b60ff860aefafc7aeb3f3bd110c781e0e6869ac2277e969e3a7b74e5")
	for v := 0; v < 4; v++ {
		sig := append(append(append([]byte{}, r...), s...), byte(v))
		rec, err := crypto.Ecrecover(dig, sig)
		if err != nil {
			fmt.Printf("v=%d: err %v\n", v, err)
			continue
		}
		fmt.Printf("v=%d: %x MATCH:%v\n", v, rec, bytes.Equal(rec, pub))
	}
}
