# 040. Read-aloud uses the system's voice, set up rather than bundled

**Status:** accepted, built 2026-10-05 — details in
[docs/agents-voices.md](../agents-voices.md)

## Context

Shrooms Agents reads replies aloud, so a long research result can be listened to
on the go. The voices installed by default are robotic. Natural neural voices
(Piper, Kokoro, via sherpa-onnx) exist and run offline on a laptop and a phone,
but a voice model is tens of megabytes, and embedding an engine means the app
keeping it current.

## Decision

- **Android uses the system's TextToSpeech engine.** The app does not bundle
  one; its voice settings explain how to install a neural engine (SherpaTTS,
  from F-Droid) and link to it and to Android's TTS settings. Installed once, it
  serves every app on the phone, not just this one.
- **Basecamp sets up Piper with one click**: the release for the machine's
  architecture and one voice (en_US lessac, medium), downloaded into the
  module's data, with `spd-say` as the fallback when it is not set up.
- **Both read sentence by sentence**, the model's text only (no tool calls),
  with pause, resume, skip and the sentence being read highlighted, and paths
  spoken as a person would say them.

## Consequences

- Nothing large ships in the APK or the module, and the quality on a phone is
  whatever engine its owner chose.
- On Basecamp the voice comes from GitHub and Hugging Face, downloaded only
  when the owner asks for it.

## What would change our mind

A phone engine good enough to make the setup step not worth asking for, or
Basecamp gaining a voice of its own.
