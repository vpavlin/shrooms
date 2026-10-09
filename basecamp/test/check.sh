#!/usr/bin/env bash
# Load the Basecamp view offscreen and assert it read a fixture.
#
# There is no display here and Basecamp cannot be driven headlessly, but the
# QML runtime can: this catches the failures that matter — a view that does not
# load, and one that loads but understands nothing.
#
#   make basecamp-check                    # uses the checked-in fixture
#   FIXTURE=/run/shrooms/status.json make basecamp-check
set -euo pipefail
cd "$(dirname "$0")/../.."

FIXTURE=${FIXTURE:-basecamp/test/status.json}
# Where the qml runtime lives depends on how Qt was installed, and this script
# runs in two places that answer differently: a nix shell here, and a
# distribution package in CI. Looking only in the nix store made the check pass
# locally and fail in CI with a message nobody reads as "Qt is somewhere else".
if [ -z "${QML:-}" ]; then
    # Every layout this has actually been run under. Debian and Ubuntu put the
    # binary under a multiarch directory rather than /usr/lib/qt6, which is why
    # the first attempt at this still failed in CI with "no qml runtime found"
    # on a machine that had just installed one.
    for candidate in \
        $(command -v qml6 2>/dev/null) \
        $(command -v qml 2>/dev/null) \
        /usr/lib/qt6/bin/qml \
        $(ls -d /usr/lib/*/qt6/bin/qml 2>/dev/null | head -1) \
        $(ls -d /usr/lib/qt6/libexec/qml 2>/dev/null | head -1) \
        $(ls -d /nix/store/*-qtdeclarative-*/bin/qml 2>/dev/null | sort -V | tail -1) \
        $(find /usr/lib /usr/lib64 /usr/libexec /usr/local/lib -maxdepth 4 \
              \( -name qml -o -name qml6 \) -type f -perm -u+x 2>/dev/null | head -1) \
        $(dpkg -L qt6-declarative-dev-tools 2>/dev/null | grep -E '/(qml|qml6)$$' | head -1)
    do
        [ -x "$candidate" ] || continue
        QML=$candidate
        break
    done
fi
if [ -z "${QML:-}" ] || [ ! -x "$QML" ]; then
    echo "no qml runtime found; set QML=/path/to/qml" >&2
    # What was actually there, because "not found" on a machine that just
    # installed Qt is a packaging question and the answer is a directory
    # listing.
    # The last candidate is a bounded find over the usual prefixes, so
    # reaching here means Qt's qml runtime is genuinely not installed rather
    # than installed somewhere this script has not heard of — which is what
    # every previous version of this message meant and did not say.
    echo "looked in: PATH, /usr/lib/qt6/bin, /usr/lib/*/qt6/bin, /nix/store," >&2
    echo "and a find under /usr/lib, /usr/lib64, /usr/libexec, /usr/local/lib" >&2
    ls -d /usr/lib/*/qt6/bin /usr/lib/qt6/* 2>/dev/null >&2 || true
    echo "files from qt6-declarative-dev-tools:" >&2
    dpkg -L qt6-declarative-dev-tools 2>/dev/null | grep -iE "bin|libexec" | head -20 >&2 || \
        echo "  (package not installed)" >&2
    exit 1
fi
echo "==> qml runtime: $QML"

# The module path sits beside the binary, but the layout differs between a nix
# store path and a distribution one, so take whichever exists.
QMLDIR=$(dirname "$(dirname "$QML")")/lib/qt-6/qml
[ -d "$QMLDIR" ] || QMLDIR=$(dirname "$(dirname "$QML")")/qml
[ -d "$QMLDIR" ] || QMLDIR=$(ls -d /usr/lib/*/qt6/qml 2>/dev/null | head -1)
[ -d "$QMLDIR" ] || QMLDIR=$(dirname "$(dirname "$QML")")/lib/qt6/qml
echo "==> qml modules: $QMLDIR"

# The fake endpoint is torn down by the same trap as the workdir, not on the
# success path. It used to be killed after the last assertion, so any earlier
# failure leaked the server — and because the port is baked into the view, the
# next run then talked to a leftover server holding a deleted directory, got a
# 404, and failed with "status is not readable JSON". One failure poisoned every
# run after it, which is a miserable thing to debug.
srv=""
work=$(mktemp -d)
cleanup() {
    [ -n "$srv" ] && kill "$srv" 2>/dev/null
    rm -rf "$work"
}
trap cleanup EXIT
cp basecamp/Main.qml basecamp/test/Harness.qml "$work/"
cp "$FIXTURE" "$work/status.json"

run() {
    # `|| true`, because a non-zero exit here is information rather than the
    # end of the run: the whole point of the assertions below is to say what
    # the view did or did not do, and set -e would abort before any of them
    # printed. A missing QML module reported itself as "make: Error 2" and not
    # one word more.
    # In one timezone wherever it runs: the fixtures' times are written in
    # +02:00 and the checks read them as a clock shows them, so on CI's UTC
    # "runs out 13:40" read 11:40 and failed (2026-10-08). A POSIX rule, not
    # a zone name: nix's Qt finds no zone database and falls back to UTC.
    TZ='CET-1CEST,M3.5.0,M10.5.0/3' QT_QPA_PLATFORM=offscreen QT_ASSUME_STDERR_HAS_CONSOLE=1 \
    QML_IMPORT_PATH="$QMLDIR" QML2_IMPORT_PATH="$QMLDIR" "$@" 2>&1 || true
}

echo "==> reads a status file sitting beside the view (the Basecamp case)"
out=$(QML_XHR_ALLOW_FILE_READ=1 run "$QML" -I "$work" "$work/Harness.qml" "$work/status.json")
echo "$out" | grep -E "^qml: (PEERS|VERSION|SERVICES|SWITCHABLE|MODE|BOUNDHERE|  )" || true
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || {
    echo "FAIL: the view loaded but read no peers"
    echo "--- what qml said ---"
    echo "${out:-(nothing at all — the runtime produced no output)}" | head -30
    echo "--- qml modules present ---"
    ls "$QMLDIR" 2>/dev/null | head -20
    exit 1
}
echo "$out" | grep -q "TypeError\|ReferenceError\|is not a" && { echo "FAIL: script errors"; echo "$out"; exit 1; }

# The sections that are not the roster. A Repeater over an empty model renders
# perfectly, so without these the check would pass on a view that understood
# none of the payload the daemon has gained since.
svcs=$(echo "$out" | sed -n 's/.*SERVICES=\([0-9]*\).*/\1/p' | head -1)
[ "${svcs:-0}" -gt 0 ] || { echo "FAIL: read no services from a fixture that has three"; exit 1; }
echo "$out" | grep -q "ssh jimmy-crib.test.mesh:22 bound" \
    || { echo "FAIL: a bound port did not render as host:port (ADR-026)"; exit 1; }
# The name has to reach the mesh the port is on. The short form is answered by
# the first mesh alone, so an unqualified name for a peer on any other mesh
# points at an address on a network it is not on — the same bug three times.
echo "$out" | grep -q "jimmy-crib.mesh:22" && { echo "FAIL: an unqualified name for a peer on a second mesh"; exit 1; }
true
echo "$out" | grep -q "http://immich.jimmy-crib.test.mesh" \
    || { echo "FAIL: an announced service did not render as a URL (ADR-023)"; exit 1; }
echo "$out" | grep -q "DNS=resolving" \
    || { echo "FAIL: did not read the daemon's name-resolution state"; exit 1; }
echo "$out" | grep -q "VERSION=v0.9.1-test" \
    || { echo "FAIL: did not read the daemon's version"; exit 1; }
# A credential that has run out and one that never arrived are different
# things, and rendering them alike sends somebody chasing a renewal.
echo "$out" | grep -q "membership nothing ended" \
    || { echo "FAIL: an expired credential did not read as ended"; exit 1; }

# The settings section. A mesh switched off has no instance behind it, so it is
# absent from everything derived from the running meshes — and the list with the
# switch on it must not be one of those. That is how a mesh went missing.
echo "$out" | grep -q "SWITCHABLE=4 RUNNING=2" \
    || { echo "FAIL: a switched-off mesh is missing from the list that can switch it on"; exit 1; }
# Lit for the option in force, dim for the other. Fixed colours are why "on"
# appeared highlighted next to a mesh that was off.
echo "$out" | grep -q "mesh test on=false lit=#6b7680" \
    || { echo "FAIL: a switched-off mesh is not drawn as off"; exit 1; }
echo "$out" | grep -q "mesh default on=true lit=#35f0a0 primary=true" \
    || { echo "FAIL: the primary mesh is not lit, or was not recognised as primary"; exit 1; }
# The config and the running process disagree between a change and the restart
# that applies it; both have to reach the view or the click looks like a no-op.
# The switch that discloses bound ports must sit next to the ports it would
# disclose, carrying the mesh label — an unqualified host names an address on
# another network.
echo "$out" | grep -q "BOUNDHERE=2 \[ssh:22@vps.default.mesh:22\*,dev:3000@vps.default.mesh:3000\*\]" \
    || { echo "FAIL: this device's bound ports are missing or misnamed"; exit 1; }
echo "$out" | grep -q "MODE=Edge RUNNING=Core ANNOUNCE=true BOUND=true RELAY=true PORTMAP=true" \
    || { echo "FAIL: did not read the configured settings alongside the running ones"; exit 1; }
# The two states between a click and the restart that applies it. Each of these
# belonged to neither list once, and each time the section emptied or doubled.
echo "$out" | grep -q "mesh pending on=true .*state=\[starts at restart\]" \
    || { echo "FAIL: a mesh switched on but not yet started is not shown as pending"; exit 1; }
echo "$out" | grep -q "mesh leaving .*state=\[left · stops at restart\]" \
    || { echo "FAIL: a mesh left but still running is not shown as leaving"; exit 1; }

# Inside Basecamp only the sibling file resolves; the two below are for running
# outside it, where there is no sandbox. Removing the sibling is what forces
# the view past its first source.
rm -f "$work/status.json"

echo "==> falls back to an absolute path when there is no sibling file"
cp "$FIXTURE" "$work/absolute.json"
out=$(QML_XHR_ALLOW_FILE_READ=1 run "$QML" -I "$work" "$work/Harness.qml" "$work/absolute.json")
echo "$out" | grep -E "^qml: PEERS" || true
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || { echo "FAIL: did not fall back to the absolute path"; echo "$out" | head -20; exit 1; }
rm -f "$work/absolute.json"

echo "==> falls back to the endpoint when no file can be read"
# Served under the name the harness points the view at, which is deliberately
# not "status.json" — that is the sibling file source 0 tries, and serving it
# here would mean the endpoint was never actually exercised.
cp "$FIXTURE" "$work/endpoint.json"

# The port is baked into the view, so it cannot simply be moved. Anything
# already holding it will answer instead of us and the test would report a
# false failure — so say what is wrong rather than testing the wrong server.
if (exec 3<>/dev/tcp/127.0.0.1/8787) 2>/dev/null; then
    exec 3<&-
    echo "FAIL: something already listens on 127.0.0.1:8787; it would answer instead of this test"
    ss -lntp 2>/dev/null | grep ':8787' || true
    exit 1
fi

( cd "$work" && exec python3 -m http.server 8787 --bind 127.0.0.1 >/dev/null 2>&1 ) &
srv=$!
sleep 1
out=$(run "$QML" -I "$work" "$work/Harness.qml" "$work/missing.json")
echo "$out" | grep -E "^qml: PEERS" || true
echo "$out" | grep -q "FILEBLOCKED=true" || { echo "FAIL: did not escalate to the endpoint"; exit 1; }
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || { echo "FAIL: fallback read no peers"; exit 1; }

echo
echo "both transports OK"

# Shrooms Agents (docs/agents.md), against a stand-in core: it only runs
# inside Basecamp, so this is the one place it is exercised before a person
# opens it. Also saves a picture of it, for looking at.
echo
echo "==> the Agents panel"
# Its own module now (basecamp-agents), with its own Main.qml: a directory of
# its own, so the two views do not overwrite each other.
mkdir -p "$work/agents"
cp basecamp-agents/Main.qml basecamp-agents/test/AgentsHarness.qml "$work/agents/"
shot=${AGENTS_SHOT:-$work/agents.png}
out=$(run "$QML" -I "$work/agents" "$work/agents/AgentsHarness.qml" "$shot")
echo "$out" | grep -E "^qml: (HOSTS|PROBED|ROWS|STREAMING|PROMPT|CALLS|ATTACHED|SENT|CONVERSATIONS|TAKEOVER|LISTWIDTH|PLANS|GLANCE|TASKROWS|TASKORDER|TASKNAME|ASKER|AGE|TASKROW1)" || true
expect() { echo "$out" | grep -qF "$1" || { echo "FAIL: $2"; exit 1; }; }
# The first one also says why, when the view did not load at all: a QML
# module the runner lacks (QtQuick.Dialogs, 2026-10-03) prints nothing else.
echo "$out" | grep -qF "HOSTS=" || { echo "$out" | head -20; echo "FAIL: the view did not load"; exit 1; }
# A script error in the view prints a warning and carries on; here it fails.
if echo "$out" | grep -E "Main.qml:[0-9]+:.*(TypeError|ReferenceError|is not a function|Cannot (read|assign))"; then
    echo "FAIL: the view hit a script error"; exit 1
fi
expect "HOSTS=1 SESSIONS=2" "the agents were not listed"
# The tasks panel's rows: grouped Needs you / Working / Stalled / Done unacked, with the
# acked one gone, the ages right, and each row carrying what a person needs to judge it.
expect "TASKROWS needs-you:m1:1h,working:m2:30m,stalled:m3:2h,unacked:m4:3h" "the task rows are not grouped and ordered as a person needs them"
expect "TASKORDER needs-you,working,stalled,unacked labels=Needs you/Working/Stalled/Done, unacked" "the task groups are not in the agreed order or named as agreed"
expect "TASKNAME named by the asker|From X: the request|the real ask|the worker's own summary|" "a task is not named by the asker's title, then the request's first line, then the summary"
expect "ASKER SPEL,jimmy,," "the asker is not read as a session from the device claim"
expect "AGE 30s,1h,10h," "the age is not in the units a person reads"
expect "TASKROW1 From Jimmy: review the module | from=jimmy to=review | latest=which of the two? | ref=laptop/review:m1" "a row does not carry its title, asker, worker, latest line and ref"
expect "LINKS pi5/jimmy>laptop/review:2:input-required:2 · needs you,laptop/shrooms>laptop/review:1:working:1" "a link does not carry the tasks on it, or the most urgent tone does not win"
expect "LOAD laptop/review=3 owed,laptop/shrooms=1 asked,pi5/jimmy=2 asked" "a card does not show what it owes and what it is waiting for"
# This device first: an agent on the machine Basecamp runs on is no peer of it.
expect "PROBED=desk|office|fdb0:9afc:a5ef:1111:2222:3333:4444:5555;laptop|office|fdb0:9afc:a5ef:388c" "this device's own agent is not looked for"
# History before the conversation, the conversation's rows in order, the
# setting note, and the prompt still open.
expect "ROWS=you,said,you,said,tool,output,note,note,you,tool,prompt" "the conversation rows are wrong"
# Opened at its tail, and read in pieces until caught up (the stand-in core
# answers four events at a time, as the real one caps its replies).
expect "WATCH=fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb shrooms 300" "the session was not opened at its tail"
expect "LOADED=11/11" "the session was not read to the end straight after opening"
expect "STILLBUSY=true true" "a search still running was taken as done"
expect "SEARCH=fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb shrooms tests FOUND=2 BUSY=false" "the search was not asked of the core, or not read back"
expect "READING=true TEXT=Earlier: the tests, in a terminal." "a result from before the agent is not shown whole"
# Row 3: two earlier lines from the transcript, the first message, then it.
expect "JUMP lit=3 row=3 kind=said searchOpen=false stick=false reach=521,0" "a search result does not jump to its message"
expect 'QUESTION open=true before=null posted={"allow":true,"answers":{"Which user?":"agent","What else?":"voice, logs"}} after=false [answered: agent; voice, logs from desk]' "a question is not offered, answered or closed"
expect 'LINKMD=see <https://pi.dev>, or [docs](https://x.io/a) and `curl http://no.pe`' "a bare URL in the model's text is not a link, or code or a link was touched"
expect '<https://already.io>' "an autolink was wrapped twice"
if echo "$out" | grep -qF '<http://in.code>' || echo "$out" | grep -qF '<<https://already.io>>'; then echo "FAIL: a URL in a code block or an autolink was made a link again"; exit 1; fi
expect 'LINKPLAIN=<span style="white-space:pre-wrap">a &lt;b&gt; &amp; <a href="http://vps.office.mesh:8099/x" style="color:#5AA9FF">http://vps.office.mesh:8099/x</a>.</span>' "a bare URL in typed text is not a link, or the text is not escaped"
expect 'LINKBOLD=see **<https://x.io/a.md>** and _<https://y.io/b>_ | <span style="white-space:pre-wrap">**<a href="https://x.io/a*b" style="color:#5AA9FF">https://x.io/a*b</a>**</span>' "emphasis around a URL was taken into the link"
expect "EXPLAIN=true,no session" "a core older than the view is not explained"
expect "OPENURL=https://example.org/a refused=true" "a link is not handed to the core to open, or a refused one is lost"
expect "FLAKY stayed=laptop:2 now=true later=false forgotten=0" "a machine that misses a round vanishes, or is never greyed or forgotten"
expect 'STARRED=laptop/shrooms rest=notes sent={"starred":true}' "a starred session is not listed first, or the star not kept on the agent"
expect 'UNSTARRED=0 sent={"starred":false}' "a session cannot be unstarred"
expect "KEPT seqs=1,2,3 kept=1759500000000 working=false then=1,2,3,4 kept=0 rows=4" "a session's kept copy is not shown while its machine is away, or not replaced once it answers"
expect 'SPEAKABLE="Results\nThe agent found three papers and go test passed.\n\n(go code)\n\nSee link for more."' "a reply is not read as prose"
expect "UNDERSCORE=built basecamp_voice_core.lgx for x86_64, really" "underscores inside a word are dropped"
expect "CZECH=true,false" "Czech is not told from English"
expect 'SPEAK sentences="Running them|\nFirst session.go, line 654 is fixed.|Then tests." key=shrooms/3 paused=true lit=true read="en:Running them|en:Running them|en:First session.go, line 654 is fixed.|en:Then tests." done=true auto=[cs:Hotovo, všechno běží.] then=[cs:Hotovo, všechno běží.|en:Second reply.] queue=0' "replies are not read sentence by sentence, paused, resumed, skipped and lit, or auto-play reads the past, tool calls, or the same reply twice"
expect "USAGE asked=/v1/usage?since=D who=laptop:2.0k,nothing:900 where=laptop:8 model=ollama/qwen3,claude-opus-5[1m] bycost=nothing,laptop busy=1h 0m since7=2026-09-29 sinceAll=[] first=laptop:true then=0" "usage is not asked of each machine in the background, or not summed by who, where and which model"
expect "GLANCE 21/0 50/1 80/2 30/2 live=62/1" "the usage link does not colour the session quota amber from half and red from 80%"
expect "PLANS n=1 windows=five_hour,seven_day merged=1:desk+laptop:rejected status=[limit reached (5 hours) — back at T] warn=[past 98% of 5 hours] label=7 days" "plan limits are not read, grouped by account or worded as on the phone"
expect "VOICE before=[Replies are read with speech-dispatcher (espeak), which sounds robotic.] during=[downloading Piper (25 MB)…] after=[Natural voice: Piper, en_US-lessac-medium. Replies are read with it.] calls=state,setup,state" "the voice section does not say what reads, or does not set the natural voice up"
expect "CARDCLICK found=true deferred=true" "a click on a session card runs its work inside the card, which a refresh can destroy"
expect "PLACEHOLDER first=[reaching laptop…] then=[no messages yet]" "an empty pane does not say it is loading, or that it is empty"
expect "REMADE watch=fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb shrooms -300 rows=0" "a copy of a session since made again is not dropped"
expect "STOPPED=/v1/sessions/shrooms/interrupt" "stopping the reply does not interrupt the session"
expect "COPYCODE links=2 first=ssh-keygen -t ed25519 copied=true said=copied 2 lines" "code in a reply does not copy itself when clicked"
expect "QUOTATAG shown=1 label=QUOTA · back T,,QUOTA" "a session out of quota is not set apart in the session list"
expect "CAGETAG shown=1 words=desktop, GitHub login" "a caged session is not marked in the session list"
expect 'ACCEPTCAGED /v1/sessions/shrooms/settings {"accept_caged":true} said=session shrooms takes tasks from caged agents' "whether a session takes tasks from caged agents cannot be set"
expect 'CAGED open=true offer=true moved=/v1/sessions/shrooms/cage {"cage":{"image":"localhost/shrooms-workbench:desktop","github":true}} out={"cage":null}' "a session is not moved into a cage with its options, or out of it"
expect "RESTARTED=/v1/sessions/shrooms/restart SAID=restarted session shrooms" "restart does not restart the session"
expect "OUTSIDE=true,heartbeat" "a turn the harness started is not marked as such"
expect "FORMCLOSED=true" "opening a session leaves the new-session form in front of it"
expect 'CAGE offered=true sent={} reset=true label=caged, desk={"image":"localhost/shrooms-workbench:desktop","nix":true} sealed={"image":"localhost/shrooms-workbench:desktop","sealed":true} words=sealed, desktop note=[moved into a cage (desktop, GitHub login) from laptop.office|taken out of its cage]' "a cage is not offered where the machine has podman, not asked for with its image and options, or its moves are not noted"
expect "HARNESS offered=claude,pi sent=pi auto=false label=[pi][]" "another harness is not offered, or a session of it not asked for"
expect "STREAMING=[Pushing **now**…] WORKING=true" "the streamed reply is not shown as it grows"
expect "CONTEXT=67% of 1M MODEL=opus-5 1m" "context and model are not read from the session"
expect "PROMPT open=true id=p1" "the waiting prompt is not offered"
# Events (the copy kept by the core) before the transcript (agentGet): asked
# first, the transcript held the pane blank for a round trip over the mesh.
expect "CALLS=agentsFind,agentWatch,agentOutbox,agentEvents,agentJobs,agentGet,agentPost,agentKeep,agentQueueFiles,agentRecord,agentUnqueue" "the core was not called as expected, or the transcript is asked for before the events are shown"
# A file and a voice note: the file is named in the next message, once, and
# the transcript lands in the composer, not sent on its own.
expect "ATTACHED=1 shot.png STICK=true" "a picked file was not attached at once, or under another name"
expect "COMPOSER=[ahoj, tady Vašek]" "the voice note's text did not land in the message box"
expect "OUTBOX=1,2 voice=voice label=[QUEUED · waiting for laptop — connect: no route to host]" "a message or voice note does not go through the outbox, or is not shown queued"
expect "CANCELLED=1" "a queued voice note cannot be taken back"
expect "VOICE first=voicenote failed=true:model not found retry=/v1/sessions/shrooms/voice/v1/retry then=you:true:ahoj notes=0" "a voice note does not show its way to a turn"
expect 'SENT="look files=/home/x/.local/share/shrooms/outbox/b-19a2b-3c4-0-shot.png" ATTACHED_AFTER=0' "the attachment was not queued with the message"
# Taking over a terminal's conversation: listed with the terminal that may
# hold it, named after its directory without colliding with "shrooms", and
# continued by id — then opened.
expect "CONVERSATIONS=2 TERMINAL=cl-logos-vpn NAME=shrooms-2" "conversations, or the name for one, are wrong"
expect "LISTWIDTH kept=true dragged=true grew=true" "the session list does not keep its width beside a long line, or does not follow the divider"
expect "TAKEOVER name=shrooms-2 resume=c-new open=shrooms-2" "taking a conversation over did not continue it by id"
expect "CONVEMPTY loaded=true n=0" "an agent with no conversations of its own still reads as looking"
# Deleting a session: the dialog says what is kept and what is cut off, and
# the session is removed by its own path, then closed.
expect "DELETETEXT=true,true" "the delete dialog does not say what is lost and what is kept"
expect "DIALOG=true" "delete did not ask first"
expect "RENAMEDIN=logos," "a rename in the stream is not followed only from the name open"
expect 'RENAMED=/v1/sessions/shrooms-2/rename {"name":"logos"} MOVED=fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb,shrooms-2,logos OPEN=logos' "renaming did not rename, move the copy, and reopen"
expect "CREDITS n=1 machines=pi5+proteus line=5.40 DIEM left today · USD -0.03" "a shared key is not one credits entry"
expect "RENEWED n=1 machines=atlas+laptop share=0.28 alone=true,0,[] glance=0" "a reading whose window reset still shows its share"
expect "TASKS note=task proj:m1 stalled — no progress after 5 reminders | 2 tasks | ⚠ a task stalled — no progress after the reminders" "tasks are not shown"
expect "FORECAST at this pace: runs out 13:40 — before it resets | at this pace: about 80% at the reset — it lasts | at this pace: about 3.0 DIEM left at the refill" "the forecast is not shown"
expect "MDHTML 111111111111111111" "Markdown is not drawn as the phone draws it"
expect "HOSTSCROLL=180" "the session list jumps on a refresh"
expect "MORE=450|— 700 earlier events not loaded · load 150 more —|— 90 earlier events not loaded · load them —" "loading more is not a step at a time"
expect "DELETED=/v1/sessions/logos OPEN=none" "deleting did not remove the open session"
expect "BOARD cards=pi5/jimmy,laptop/shrooms,laptop/notes,proteus/review edges=proteus/review>laptop/notes:input-required,laptop/shrooms>pi5/jimmy:stalled,pi5/jimmy>proteus/review:working drawn=3 list=false flow=true tasks=3" "the board does not show every session, or the links between agents working on each other's tasks"
expect "BOARDOPEN open=jimmy withlist=true esc=true,true back=true,true,true,false" "a card on the board does not open its session beside the list, Esc does not bring the board back (or takes it from a dialog), or the board does not come back in its place"
echo "agents panel OK${AGENTS_SHOT:+ (picture: $shot)}"
