# 007. Separate device and WireGuard keys

**Status:** accepted

## Context

One Ed25519 keypair could serve as both the libp2p/control-plane identity and
(converted to X25519) the WireGuard static key. It is mechanically clean:
Ed25519 → X25519 conversion is standard, and the Ed25519 scalar is already in
the clamped form WireGuard expects.

## Decision

Two independent keypairs per device: Ed25519 for identity and signing, X25519
for WireGuard.

## Why

The cryptography is *probably* fine. Thormarker
([eprint 2021/509](https://eprint.iacr.org/2021/509)) proves joint security for
Ed25519 signatures alongside an X25519 KEM in the ROM, without even assuming
domain separation. But nothing published covers Ed25519 signing alongside
`Noise_IKpsk2` specifically, and Valsorda declines to call the conversion safe
in general.

**The decisive argument is not cryptographic.** WireGuard needs the raw scalar
in memory to perform DH. If the device key is also the WireGuard key, it can
never live in a TPM, Secure Enclave or YubiKey as a non-exportable key — which
is the single best mitigation against a stolen laptop, and worth more than any
protocol change.

Two supporting reasons: independent rotation (rotate the WireGuard key after a
scare without changing identity or overlay address), and blast radius —
`wg showconf`, config backups and `/etc/wireguard/*.conf` move WireGuard keys
around casually, and an identity key should never be in those places.

The cost of separation is 32 bytes in a message we are already signing.

## Consequences

- Two keys to persist and back up.
- The binding between them is the signed announce: the device key signs a
  message naming the WireGuard key. That signed tuple *is* the certificate.
