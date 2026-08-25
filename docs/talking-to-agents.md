# Talking to agents, out loud, from a phone

**Status:** thinking out loud, Vaclav's idea, 2026-08-25. Decisions at the end
are his. Nothing built.

The want: send voice messages back and forth with a handful of agents — some
Claude, some pi.dev, some whatever comes next — from a phone, over the mesh.

## Most of this already exists, and it is the part people usually get wrong

The hard parts of "a private messaging system for a small group" are addressing,
identity, authentication, discovery and reachability. This mesh has all five,
and they are not approximations:

- **Addressing.** Every device has a stable overlay address and a `.internal`
  name that follows it between networks.
- **Identity and authentication.** WireGuard authenticates every packet, and
  membership is an admin-signed credential (ADR-018). An agent on the mesh is
  already a known party; there is no account to make, no token to mint, no
  bearer secret to leak.
- **Reachability.** Direct where possible, relayed where not, without either end
  knowing which.
- **Discovery by *kind*.** Services carry a type (RFC 6335, ≤15 characters) and
  a peer can be asked *what does this mesh offer of type X*.

So an agent is not a new concept. **An agent is a service on the mesh**, and the
phone's "list of agents" is one query against machinery that already shipped:

    shrooms services add voice --type=agent --port 8666

The thing that would otherwise be a month of authentication and NAT misery is
already done, and it is done better than a greenfield app would have done it.

## The one decision that shapes everything: where speech becomes text

Everything else follows from this, so it goes first.

**Agents are text-native.** Claude Code takes text. pi.dev, whatever its
interface turns out to be, takes text. A shell script takes text. The moment the
protocol requires an agent host to transcribe audio, the bar for participating
becomes *run a speech model*, and most of the agents on this list will not clear
it. "A general method" and "every agent needs whisper" cannot both be true.

Three ways to arrange it:

**A. The phone transcribes, and speaks the reply.** Android has both halves
built in — on-device recognition and text-to-speech — so this needs no model, no
service and no cloud. Agents exchange text and never learn that audio was
involved. The cost: the audio is gone, so it is voice-*driven* text chat rather
than voice messages.

**B. The agent host transcribes.** True voice messages, and every agent host now
needs a speech stack. This is the option that fails the "general method" test.

**C. The phone transcribes, and sends both.** The message carries the audio *and*
the transcript. Agents read the transcript and ignore the audio entirely; the
phone keeps and plays the audio. Replies come back as text and the phone speaks
them.

**C is the recommendation, and the reason is the bar it sets.** The minimum an
agent must be able to do is *read a string over HTTP and write one back* —
clearable by a bash script — while the human still gets a real voice thread with
real audio in it. The audio is for the person, the text is for the agent, and
neither is pretending to be the other.

A pleasant side effect: since the phone speaks the replies, each agent can be
given a different system voice. That is a one-line distinction that makes a list
of agents feel like a list of *someones*, and it costs nothing.

## Two ways to build it, and they are not equally sized

### Don't build an app: Matrix

A homeserver on the mesh, one room per agent, each agent a bot. Element already
does voice messages, threads, history, multiple devices and encryption. This
gets to "I am talking to my agents from my phone" without writing a client at
all, and the agent side is a small bot per runtime.

The cost is honest: a homeserver is a real dependency for one human and a few
bots, and the lighter Rust servers (conduit and its forks) move around enough
that the current state is worth checking rather than assuming. And Element is a
chat client — it will never be *a list of my agents*, because that is not what it
is for.

### Build the small app

If the phone does speech (option C), the app is genuinely small: record,
transcribe, POST, poll, play, speak. The list of agents is a service query. The
list of messages is a local database. There is no account system, no push
infrastructure and no server, because the mesh supplies all three.

**My honest read: Matrix is the right spike, the small app is the right end
state.** The uncertain thing here is not whether audio can be moved between two
machines — it obviously can — it is whether talking to agents this way is
actually pleasant, or whether it turns out that typing was better all along.
Matrix answers that in an evening without committing to anything. If the answer
is yes, the app is a week and it is the one that can be *about* agents.

## A protocol small enough that anything can speak it

HTTP over the mesh, because every language has a client and a server, and the
mesh has already authenticated both ends:

    POST /v1/messages       deliver one message
    GET  /v1/messages?since= poll for replies (long-poll or SSE for push)

```json
{
  "id": "01J...",
  "conversation": "01J...",
  "from": "phone.internal",
  "at": "2026-08-25T10:04:11Z",
  "text": "did the deploy finish?",
  "audio": { "type": "audio/ogg; codecs=opus", "bytes": 41233 }
}
```

Audio as a second request or a multipart part rather than base64 in the JSON —
a minute of Opus is around 200 KB, which is nothing for the tunnel and a lot to
inflate by a third for no reason.

**`text` is mandatory, `audio` is optional.** That one rule is what keeps the
bar low enough for every agent to clear: a reply with no audio is an ordinary
reply, and an agent that never sends audio is a first-class participant.

## Agents are not always running, so something has to hold the message

A Claude Code session exists while somebody is running it. A message sent to one
that is closed has to go somewhere.

The natural answer here is a small resident process per host — the thing that
publishes the `agent` service, holds a mailbox, and wakes the actual runtime when
something arrives. It is also the natural place for the per-runtime adapter,
which is where Claude and pi.dev stop looking alike.

If the *host* is off entirely, the phone queues and sends when the peer returns.
The roster already knows who is online, and as of today it knows across restarts
— so "send when they come back" is a question the mesh can already answer rather
than a new subsystem.

A central mailbox on the always-on node would also work and is less code, at the
cost of a component everything depends on. Worth naming, given how hard the rest
of this project works to avoid exactly that.

## What I do not know

**What pi.dev's interface actually is.** I have not seen it and will not guess.
Whether it exposes an HTTP endpoint, a CLI, or something that has to be driven
interactively decides how thin the adapter can be, and it is the main thing that
could make "general" harder than it looks here.

## Decisions

1. **Voice-driven text (A), or real voice with transcripts (C)?** C costs one
   audio blob per message and keeps the recordings; A is simpler and throws them
   away. This is the fork everything else hangs off.
2. **Matrix spike first, or straight to the app?**
3. **Mailbox per host, or one on the always-on node?** Distributed matches the
   project; central is less code.
4. **One agent per service, or one service with many agents behind it?** A
   laptop running three agents is either three registrations or one with a
   roster of its own.
