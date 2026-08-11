package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

// Encryption at rest for the admin key.
//
// A passphrase rather than the system keystore, deliberately. Secret Service
// needs D-Bus and an unlocked login session — so a stolen powered-on laptop
// still yields the key — and it does not exist on a headless machine at all. A
// passphrase costs one prompt on a key used a few times a year, works
// everywhere including over ssh, and means a stolen laptop is not a stolen
// mesh.
//
// A Keycard is the better answer where there is one, and this is what the rest
// of us use. Neither is required: an unencrypted file is still allowed, because
// a key you cannot open is worse than a key someone might steal, and forcing a
// passphrase on someone who keeps the file on an encrypted volume is theatre.
const (
	// scrypt cost. 2^17 is a few hundred milliseconds on a laptop, which is
	// nothing once a year and expensive a few billion times.
	scryptN = 1 << 17
	scryptR = 8
	scryptP = 1
	saltLen = 16
)

// encryptAdminKey seals the private key under a passphrase.
func encryptAdminKey(priv []byte, passphrase string) (string, error) {
	if passphrase == "" {
		return "", errors.New("no passphrase")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, chacha20poly1305.KeySize)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	// salt || nonce || ciphertext, so the file carries everything needed to
	// open it except the passphrase.
	out := append([]byte(nil), salt...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, priv, nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// decryptAdminKey opens a sealed key.
func decryptAdminKey(sealed, passphrase string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("admin key is unreadable: %w", err)
	}
	aead, err := chacha20poly1305.NewX(make([]byte, chacha20poly1305.KeySize))
	if err != nil {
		return nil, err
	}
	if len(raw) < saltLen+aead.NonceSize() {
		return nil, errors.New("admin key is truncated")
	}
	salt, rest := raw[:saltLen], raw[saltLen:]
	nonce, ct := rest[:aead.NonceSize()], rest[aead.NonceSize():]

	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	real, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	priv, err := real.Open(nil, nonce, ct, nil)
	if err != nil {
		// One message for a wrong passphrase and a corrupt file alike: telling
		// them apart tells someone guessing which half they got right.
		return nil, errors.New("wrong passphrase, or the admin key is damaged")
	}
	return priv, nil
}
