// Drives the core's agent hub against a real shrooms-agent, the way the view
// will: find, read, follow. Run by test/agents_check.sh; not part of the build.
#include "../core/src/shrooms_agents.h"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <unistd.h>
#include <sys/stat.h>
#include <thread>

using namespace agents;

static int fails = 0;
#define CHECK(c, ...) do { if (!(c)) { std::printf("FAIL %s: ", #c); std::printf(__VA_ARGS__); std::printf("\n"); fails++; } else std::printf("ok   %s\n", #c); } while (0)

int main(int argc, char** argv)
{
    if (argc < 3) { std::printf("usage: agents_check <address> <session>\n"); return 2; }
    std::string addr = argv[1], session = argv[2];

    CHECK(isMeshAddress(addr), "%s", addr.c_str());
    CHECK(!isMeshAddress("128.140.55.128"), "public address accepted");
    CHECK(!isMeshAddress("laptop.office.mesh"), "a name accepted");
    CHECK(isMeshAddress("198.19.245.139"), "alias refused");
    CHECK(!safePath("/v1/../../etc"), "dotdot");
    CHECK(!safePath("/v1/x HTTP/1.0\r\nX: y"), "header injection");
    CHECK(!safePath("/admin"), "outside the API");
    // Programs the core starts get the environment without the AppImage's
    // loader settings: with them, coreutils refused to run and xdg-open failed.
    {
        setenv("LD_PRELOAD", "/tmp/.mount_x/usr/lib/libprocself_fix.so", 1);
        setenv("__BUNDLE_REAL_EXE", "/tmp/.mount_x/usr/bin/.logos_host.elf", 1);
        setenv("LD_LIBRARY_PATH", "/tmp/.mount_x/usr/lib", 1);
        auto e = childEnv();
        std::string all;
        for (auto& v : e.vars) all += v + "\n";
        CHECK(all.find("LD_PRELOAD=") == std::string::npos && all.find("__BUNDLE_REAL_EXE=") == std::string::npos &&
              all.find("LD_LIBRARY_PATH=") == std::string::npos, "loader settings passed on");
        CHECK(all.find("PATH=") != std::string::npos && e.ptrs.back() == nullptr, "the rest kept");
        unsetenv("LD_PRELOAD"); unsetenv("__BUNDLE_REAL_EXE"); unsetenv("LD_LIBRARY_PATH");
    }

    // Links the core opens for a view: web links only.
    CHECK(safeUrl("https://github.com/users/vpavlin/packages/container/shrooms-agent/settings"), "a plain https link");
    CHECK(safeUrl("http://vps.office.mesh:8099/x"), "a mesh http link");
    CHECK(!safeUrl("file:///etc/passwd"), "a file URL");
    CHECK(!safeUrl("javascript:alert(1)"), "a script URL");
    CHECK(!safeUrl("https://x.io/a b"), "a space, which xdg-open would split");
    CHECK(!safeUrl("https://x.io/\n--option"), "a newline");
    CHECK(!safeUrl("-https://x.io"), "an option");

    // A copy keeps strings cut, never mid-escape or mid-character.
    {
        std::string big(5000, 'x');
        std::string ev = "{\"seq\":7,\"data\":{\"text\":\"" + std::string(4094, 'a') + "\\n\u0161\u0161" + big + "\",\"k\":\"short\"}}";
        std::string t = trimStrings(ev, 4096);
        CHECK(t.size() < 4300 && t.find("\"k\":\"short\"") != std::string::npos && t.find("\xe2\x80\xa6\"") != std::string::npos,
              "trimmed to %zu: %s", t.size(), t.substr(4080, 60).c_str());
        CHECK(t.find("\\n") == std::string::npos || t.find("\\n") < 4200, "an escape split");
        CHECK(trimStrings("{\"a\":\"\\\"q\\\"\"}", 4096) == "{\"a\":\"\\\"q\\\"\"}", "a short string with escapes changed");
    }

    // Reading aloud: the engine runs as a process group of its own, says it
    // is speaking until it ends, and stop ends all of it. A stand-in Piper in
    // a data folder of its own, silent, so the check makes no sound.
    {
        char tmpl[] = "/tmp/speak-check-XXXXXX";
        std::string data = ::mkdtemp(tmpl);
        std::string saved = std::getenv("XDG_DATA_HOME") ? std::getenv("XDG_DATA_HOME") : "";
        setenv("XDG_DATA_HOME", data.c_str(), 1);
        std::string dir = data + "/shrooms/piper";
        std::system(("mkdir -p " + dir + " && printf '#!/bin/sh\\ncat > " + data + "/said\\nsleep ${FAKE_PIPER_SLEEP:-30}\\n' > " + dir +
                     "/piper && chmod +x " + dir + "/piper && : > " + dir + "/en.onnx && : > " + dir + "/cs.onnx").c_str());
        Hub h;
        std::string engine;
        std::string why = h.speak("Hello from the mesh.", false);
        std::this_thread::sleep_for(std::chrono::milliseconds(400));
        bool on = h.speaking(engine);
        CHECK(why.empty() && on && engine == "piper", "speak: why=%s on=%d engine=%s", why.c_str(), on, engine.c_str());
        FILE* f = std::fopen((data + "/said").c_str(), "r");
        char buf[128] = {0};
        if (f) { std::fread(buf, 1, sizeof buf - 1, f); std::fclose(f); }
        CHECK(std::string(buf) == "Hello from the mesh.", "the engine was given: %s", buf);
        auto t0 = std::chrono::steady_clock::now();
        h.speakStop();
        long ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count();
        CHECK(!h.speaking(engine) && ms < 2000, "stop took %ld ms, still speaking %d", ms, h.speaking(engine));
        // One that ends by itself is reported done.
        setenv("FAKE_PIPER_SLEEP", "0", 1);
        h.speak("Short.", false);
        bool done = false;
        for (int i = 0; i < 30 && !done; i++) {
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
            done = !h.speaking(engine);
        }
        CHECK(done, "a finished reading still says speaking");
        unsetenv("FAKE_PIPER_SLEEP");
        if (saved.empty()) unsetenv("XDG_DATA_HOME"); else setenv("XDG_DATA_HOME", saved.c_str(), 1);
        std::system(("rm -rf " + data).c_str());
    }

    std::string out, err;
    bool ok = request(addr, "GET", "/v1/sessions", "", 5, out, err);
    CHECK(ok && out.find("\"sessions\"") != std::string::npos, "%s %s", err.c_str(), out.substr(0, 80).c_str());
    ok = request(addr, "GET", "/v1/sessions/no-such-session/history", "", 5, out, err);
    CHECK(!ok && err.find("404") != std::string::npos && err.find("no session") != std::string::npos,
          "error: %s", err.c_str());
    ok = request("fd00::1", "GET", "/v1/sessions", "", 1, out, err);
    CHECK(!ok, "an unreachable agent answered");

    Hub hub;
    hub.find("laptop|office|" + addr + ";bogus|office|fd00::1;public|x|8.8.8.8");
    std::string found;
    for (int i = 0; i < 50; i++) {
        found = hub.found();
        if (found.find(addr) != std::string::npos) break;
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    CHECK(found.find("\"name\":\"laptop\"") != std::string::npos && found.find("\"list\":{") != std::string::npos,
          "%s", found.substr(0, 200).c_str());
    CHECK(found.find("8.8.8.8") == std::string::npos, "a public address was probed");

    // Switching away from a machine that does not answer does not wait for its
    // connection attempt: that froze Basecamp, its call timing out at 20 s.
    // fdb0:9afc:a5ef:ffff::1 is on the mesh's prefix and nobody's address.
    {
        Hub h;
        h.watch("fdb0:9afc:a5ef:ffff::1", "nowhere", 300);
        std::this_thread::sleep_for(std::chrono::milliseconds(300));
        auto t0 = std::chrono::steady_clock::now();
        h.watch(addr, session, -5);
        long ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count();
        CHECK(ms < 1000, "switching away took %ld ms", ms);
    }

    // A copy kept here far behind the session — it went on from the phone —
    // is not caught up from, which replayed thousands of events into the
    // view piece by piece (2026-10-06): it opens at the end instead.
    {
        std::ofstream(Hub::historyPath(addr, session), std::ios::trunc)
            << "{\"saved\":1}\n{\"seq\":1,\"kind\":\"message\",\"data\":{\"text\":\"old\"}}\n"
            << "{\"seq\":2,\"kind\":\"message\",\"data\":{\"text\":\"old\"}}\n";
        hub.watch(addr, session, 300);
        std::string r;
        for (int i = 0; i < 100; i++) {
            r = hub.events(0);
            if (r.find("\"seq\":") != std::string::npos) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
        long long first = 0;
        size_t at = r.find("\"seq\":");
        if (at != std::string::npos) first = std::atoll(r.c_str() + at + 6);
        CHECK(r.find("\"kept\":0") != std::string::npos && first > 1000,
              "a copy far behind was shown and caught up from: first %lld, %s", first, r.substr(0, 160).c_str());
    }

    hub.watch(addr, session, 0);
    std::string ev;
    for (int i = 0; i < 50; i++) {
        ev = hub.events(0);
        if (ev.find("\"seq\":") != std::string::npos) break;
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    // From the first event — or, on a session longer than the core keeps
    // (kKeepEvents, 4000), from the first it keeps: by the time this reads,
    // the follower may have filled the window and dropped the start.
    long long listedLast = 0;
    {
        size_t at = found.find("{\"name\":\"" + session + "\",\"dir\":");
        size_t ls = at == std::string::npos ? at : found.find("\"last_seq\":", at);
        if (ls != std::string::npos) listedLast = std::atoll(found.c_str() + ls + 11);
    }
    long long firstSeen = 0;
    {
        size_t at = ev.find("\"seq\":");
        if (at != std::string::npos) firstSeen = std::atoll(ev.c_str() + at + 6);
    }
    CHECK(ev.find("\"connected\":true") != std::string::npos &&
              (firstSeen == 1 || (listedLast > 4000 && firstSeen > 1 && firstSeen <= listedLast - 4000 + 1 + 50)),
          "%s", ev.substr(0, 200).c_str());
    // Read to the end (the core answers in pieces); after that, only what
    // the session has said since — it may be this one, and talking.
    long long next = std::atoll(ev.c_str() + 8);
    for (int i = 0; i < 10000 && hub.events(next).find("\"more\":true") != std::string::npos; i++)
        next = std::atoll(hub.events(next).c_str() + 8);
    next = std::atoll(hub.events(next).c_str() + 8);
    std::string more = hub.events(next);
    CHECK(std::atoll(more.c_str() + 8) >= next, "next went backwards: %s", more.substr(0, 120).c_str());

    // The whole backlog, read in pieces: no reply much over half a megabyte,
    // and every event once, in order.
    {
        for (int i = 0; i < 50; i++) {
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
            if (hub.events(0).find("\"more\":true") == std::string::npos && i > 10) break;
        }
        // The core keeps the last 4000 events, so a longer session starts
        // later than 1; from there, every event once, in order.
        long long at = 0, first = 0, last = 0, pieces = 0, overs = 0;
        size_t biggest = 0;
        bool ordered = true, more = true;
        while (more && pieces < 10000) {
            std::string r = hub.events(at);
            pieces++;
            biggest = std::max(biggest, r.size());
            more = r.find("\"more\":true") != std::string::npos;
            at = std::atoll(r.c_str() + 8);
            long long n = 0;
            // Each event starts {"seq":; inside a string it would be {\"seq\":.
            for (size_t p = r.find("{\"seq\":"); p != std::string::npos; p = r.find("{\"seq\":", p + 1)) {
                long long s = std::atoll(r.c_str() + p + 7);
                if (first == 0) first = s;
                else if (s != last + 1) ordered = false;
                last = s;
                n++;
            }
            // Over half a megabyte only when one event is that big by itself:
            // a reply is cut between events, never inside one.
            if (r.size() > 600 * 1024 && n > 1) overs++;
        }
        std::printf("     backlog: events %lld..%lld in %lld pieces, the biggest %zu bytes\n", first, last, pieces, biggest);
        CHECK(ordered && last > 0, "events skipped or repeated, %lld..%lld", first, last);
        CHECK(overs == 0, "%lld replies over the size with more than one event in them", overs);

        // Opened at its tail: the last five, nothing before (no copy kept of
        // it yet — with one, it opens at the copy).
        Hub::forgetHistory(addr, session);
        hub.watch(addr, session, -5);
        std::string t;
        for (int i = 0; i < 50; i++) {
            t = hub.events(0);
            if (t.find("\"seq\":") != std::string::npos) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(500));
        t = hub.events(0);
        size_t p = t.find("{\"seq\":");
        long long tailFirst = p == std::string::npos ? -1 : std::atoll(t.c_str() + p + 7);
        CHECK(tailFirst >= last - 4 && tailFirst > 1, "the tail started at %lld of %lld", tailFirst, last);
    }

    // What is shown of a session is kept on disk, and shown — marked — when
    // its machine cannot be reached; replaced once it answers.
    {
        std::string kept = Hub::historyPath(addr, session);
        std::remove(kept.c_str());
        hub.watch(addr, session, 5);
        std::this_thread::sleep_for(std::chrono::milliseconds(1500));
        hub.watch(addr, "no-such-session", 5);  // leaving it keeps it, at the latest
        FILE* f = std::fopen(kept.c_str(), "r");
        int lines = 0;
        char buf[1 << 16];
        std::string firstLine;
        while (f && std::fgets(buf, sizeof buf, f)) {
            if (lines++ == 0) firstLine = buf;
        }
        if (f) std::fclose(f);
        CHECK(firstLine.compare(0, 9, "{\"saved\":") == 0 && lines >= 2 && lines <= 6,
              "kept: %d lines, first %s", lines, firstLine.c_str());

        // The same, as for a machine that does not answer.
        std::string away = Hub::historyPath("fd00::1", session);
        std::rename(kept.c_str(), away.c_str());
        hub.watch("fd00::1", session, 5);
        std::string r = hub.events(0);
        CHECK(r.find("\"connected\":false") != std::string::npos && r.find("\"kept\":0") == std::string::npos &&
              r.find("{\"seq\":") != std::string::npos, "offline: %s", r.substr(0, 160).c_str());
        // Opened at the whole history, nothing kept is shown: it is not what
        // was asked for.
        hub.watch("fd00::1", session, 0);
        r = hub.events(0);
        CHECK(r.find("{\"seq\":") == std::string::npos, "kept shown for the whole history");

        // Kept, then continued: the copy first, then only what came after it
        // — every event once, in order, from the copy's first.
        std::rename(away.c_str(), kept.c_str());
        hub.watch(addr, session, 5);
        r = hub.events(0);
        size_t p0 = r.find("{\"seq\":");
        long long keptFirst = p0 == std::string::npos ? -1 : std::atoll(r.c_str() + p0 + 7);
        CHECK(r.find("\"kept\":0") == std::string::npos && keptFirst > 0, "not shown kept first: %s", r.substr(0, 160).c_str());
        for (int i = 0; i < 50; i++) {
            r = hub.events(0);
            if (r.find("\"kept\":0") != std::string::npos) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(500));
        r = hub.events(0);
        long long prev = 0, n = 0, firstSeen = 0;
        bool once = true;
        for (size_t q = r.find("{\"seq\":"); q != std::string::npos; q = r.find("{\"seq\":", q + 1)) {
            long long sq = std::atoll(r.c_str() + q + 7);
            if (firstSeen == 0) firstSeen = sq;
            if (prev && sq != prev + 1) once = false;
            prev = sq;
            n++;
        }
        CHECK(r.find("\"kept\":0") != std::string::npos && firstSeen == keptFirst && once && n >= 5,
              "not continued from the copy: first %lld (copy %lld), %lld events, in order %d: %s",
              firstSeen, keptFirst, n, once, r.substr(0, 160).c_str());

        Hub::forgetHistory(addr, session);
        CHECK(std::fopen(kept.c_str(), "r") == nullptr, "forgotten, still there");
    }

    // Go escapes <, > and & as \u sequences: they must come back as written.
    CHECK(field("{\"text\":\"A \\u0026 B \\u003cx\\u003e \\\"q\\\" Vašek\"}", "text") == "A & B <x> \"q\" Vašek",
          "%s", field("{\"text\":\"A \\u0026 B \\u003cx\\u003e\"}", "text").c_str());

    // A file, in the background, to the session's machine.
    std::string local = "/tmp/agents-check-upload.txt";
    { FILE* f = std::fopen(local.c_str(), "w"); std::fputs("hello from the check\n", f); std::fclose(f); }
    long id = hub.upload(addr, session, local);
    std::string jobs;
    for (int i = 0; i < 50; i++) {
        jobs = hub.jobs();
        if (jobs.find("\"state\":\"pending\"") == std::string::npos) break;
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    CHECK(id > 0 && jobs.find("\"state\":\"done\"") != std::string::npos && jobs.find("/uploads/" + session + "/") != std::string::npos,
          "%s", jobs.c_str());
    std::remove(local.c_str());
    hub.upload(addr, session, "/nonexistent");
    std::this_thread::sleep_for(std::chrono::milliseconds(300));
    CHECK(hub.jobs().find("is not a file") != std::string::npos, "%s", hub.jobs().c_str());

    // Pasting an image: says what to install when the clipboard tool is
    // missing, and reports no image (so text pastes as usual) when there is
    // none — never a silent nothing.
    {
        std::string perr;
        std::string kept = hub.pasteImage(perr);
        CHECK(!kept.empty() || perr.empty() || perr.find("sudo apt install") != std::string::npos,
              "paste: %s %s", kept.c_str(), perr.c_str());
        std::printf("     paste: %s\n", !kept.empty() ? "an image kept" : (perr.empty() ? "no image on the clipboard" : perr.c_str()));
        if (!kept.empty()) ::unlink(kept.c_str());
    }

    // A voice note: two seconds from the real microphone, transcribed on the
    // agent's machine. Only whether it went through is checked; what the room
    // said is not printed.
    if (std::getenv("AGENTS_CHECK_VOICE")) {
        std::string why = hub.recordStart();
        CHECK(why.empty() && hub.jobs().find("\"recording\":true") != std::string::npos, "%s", why.c_str());
        std::this_thread::sleep_for(std::chrono::seconds(2));
        std::string err;
        long vid = hub.recordStop(addr, session, "en", err);
        CHECK(vid > 0, "%s", err.c_str());
        for (int i = 0; i < 300; i++) {
            jobs = hub.jobs();
            if (jobs.find("\"kind\":\"voice\",\"state\":\"pending\"") == std::string::npos) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
        CHECK(jobs.find("\"kind\":\"voice\",\"state\":\"done\"") != std::string::npos &&
              jobs.find("voice.wav") != std::string::npos && jobs.find("\"recording\":false") != std::string::npos,
              "voice job did not finish: %s", jobs.substr(jobs.find("voice") == std::string::npos ? 0 : jobs.find("voice")).substr(0, 200).c_str());
    }

    // A recorder that dies at once is passed over for the next; one that runs
    // and records nothing is said to have done so, with its own words, and
    // nothing is queued (the Duet: "the file is empty", 2026-10-06).
    {
        char tmpl[] = "/tmp/fake-rec-XXXXXX";
        std::string bin = ::mkdtemp(tmpl);
        std::ofstream(bin + "/pw-record") << "#!/bin/sh\necho 'no default source' >&2\nexit 1\n";
        std::ofstream(bin + "/parecord") << "#!/bin/sh\necho 'Stream error: No such entity' >&2\ntrap 'exit 0' INT\nwhile :; do sleep 0.1; done\n";
        (void)!std::system(("chmod +x " + bin + "/pw-record " + bin + "/parecord").c_str());
        std::string oldPath = std::getenv("PATH") ? std::getenv("PATH") : "";
        setenv("PATH", (bin + ":/usr/bin:/bin").c_str(), 1);
        char dtmpl[] = "/tmp/fake-rec-data-XXXXXX";
        std::string data = ::mkdtemp(dtmpl);
        setenv("XDG_DATA_HOME", data.c_str(), 1);
        {
            Hub h;
            std::string why = h.recordStart();
            CHECK(why.empty(), "no recorder after pw-record failed: %s", why.c_str());
            std::this_thread::sleep_for(std::chrono::milliseconds(300));
            std::string err;
            std::string id = h.recordSend("fd00::1", "nowhere", err);
            CHECK(id.empty() && err.find("nothing was recorded (parecord)") != std::string::npos &&
                      err.find("No such entity") != std::string::npos,
                  "empty recording: id=%s err=%s", id.c_str(), err.c_str());
            CHECK(h.outbox() == "[]", "an empty recording was queued: %s", h.outbox().c_str());
        }
        setenv("PATH", oldPath.c_str(), 1);
        unsetenv("XDG_DATA_HOME");
        (void)!std::system(("rm -rf " + bin + " " + data).c_str());
    }

    // A fresh machine has no ~/.local/share yet: what is kept under it (the
    // outbox, kept copies, a file attached) makes every missing parent, as
    // mkdir -p does, rather than failing at the first.
    {
        char tmpl[] = "/tmp/fresh-home-XXXXXX";
        std::string home = ::mkdtemp(tmpl);
        std::string share = home + "/.local/share";
        setenv("XDG_DATA_HOME", share.c_str(), 1);
        std::string kept = Hub::historyPath("fd00::1", "fresh");
        struct stat st {};
        CHECK(::stat((share + "/shrooms/history").c_str(), &st) == 0 && S_ISDIR(st.st_mode),
              "no history directory made under a missing %s (%s)", share.c_str(), kept.c_str());
        CHECK(makeDirs(home + "/a/b/c") && makeDirs(home + "/a/b/c"), "makeDirs, made and made again");
        std::ofstream(home + "/file") << "x";
        CHECK(!makeDirs(home + "/file") && !makeDirs(home + "/file/below"), "a file taken for a directory");
        std::string rm = "rm -rf " + home;
        (void)!std::system(rm.c_str());
        unsetenv("XDG_DATA_HOME");
    }

    // The outbox, in a directory of its own (XDG_DATA_HOME): written to an
    // unreachable machine it waits, with the reason, across a restart of the
    // core; written to a reachable one it arrives, once.
    {
        char tmpl[] = "/tmp/outbox-check-XXXXXX";
        std::string data = ::mkdtemp(tmpl);
        setenv("XDG_DATA_HOME", data.c_str(), 1);
        {
            Hub h;
            std::string id = h.queueText("fd00::1", "nowhere", "written offline\twith a tab");
            std::string ob;
            for (int i = 0; i < 100; i++) {
                ob = h.outbox();
                if (ob.find("\"error\":\"\"") == std::string::npos) break;
                std::this_thread::sleep_for(std::chrono::milliseconds(100));
            }
            CHECK(ob.find(id) != std::string::npos && ob.find("written offline\\twith a tab") != std::string::npos &&
                  ob.find("\"error\":\"\"") == std::string::npos, "queued and failing: %s", ob.c_str());
        }
        {
            Hub h; // a restart: read back from disk
            std::string ob = h.outbox();
            CHECK(ob.find("written offline\\twith a tab") != std::string::npos, "lost across a restart: %s", ob.c_str());
            std::string id = ob.substr(ob.find("\"id\":\"") + 6);
            id = id.substr(0, id.find('"'));
            CHECK(h.unqueue(id) && h.outbox() == "[]", "cancel: %s", h.outbox().c_str());
        }
        // Delivered, to a session of its own on the agent (pi: no model call
        // needed for the message to be taken).
        std::string out, err;
        request(addr, "POST", "/v1/sessions", "{\"name\":\"outbox-check\",\"dir\":\"/tmp\",\"harness\":\"pi\"}", 5, out, err);
        {
            Hub h;
            std::string id = h.queueText(addr, "outbox-check", "from the outbox");
            for (int i = 0; i < 100 && h.outbox() != "[]"; i++) std::this_thread::sleep_for(std::chrono::milliseconds(100));
            CHECK(h.outbox() == "[]", "not sent: %s", h.outbox().c_str());
            request(addr, "GET", "/v1/sessions/outbox-check/history", "", 5, out, err);
            std::string ev;
            int fd = -1;
            (void)fd;
            bool ok = request(addr, "POST", "/v1/sessions/outbox-check/messages",
                              "{\"text\":\"from the outbox\",\"id\":\"" + id + "\"}", 5, ev, err);
            CHECK(ok && ev.find("\"duplicate\":true") != std::string::npos, "sent again was not a duplicate: %s %s",
                  ev.c_str(), err.c_str());
        }
        // A file attached with the machine unreachable waits in the outbox
        // with its message, naming nothing yet; cancelled, it goes with it.
        {
            Hub h;
            std::string src = data + "/notes.txt";
            std::ofstream(src) << "attached text";
            std::string e;
            std::string kept = h.keepFile(src, e);
            CHECK(!kept.empty() && kept.compare(0, h.keptDir().size(), h.keptDir()) == 0, "kept: %s %s", kept.c_str(), e.c_str());
            std::string id = h.queueText("fd00::1", "nowhere", "", {kept});
            std::string ob = h.outbox();
            CHECK(ob.find("\"files\":[{\"name\":\"notes.txt\",\"sent\":false}]") != std::string::npos,
                  "queued with its file: %s", ob.c_str());
            CHECK(h.unqueue(id) && ::access(kept.c_str(), F_OK) != 0, "the kept file stays after a cancel");
        }
        // Reachable: the file is uploaded first, once, and the message names
        // where the agent kept it.
        {
            Hub h;
            std::string src = data + "/report.txt";
            std::ofstream(src) << "the report";
            std::string e;
            std::string kept = h.keepFile(src, e);
            h.queueText(addr, "outbox-check", "see the file", {kept});
            for (int i = 0; i < 100 && h.outbox() != "[]"; i++) std::this_thread::sleep_for(std::chrono::milliseconds(100));
            CHECK(h.outbox() == "[]", "not sent: %s", h.outbox().c_str());
            CHECK(::access(kept.c_str(), F_OK) != 0, "the kept copy outlived its message");
            request(addr, "GET", "/v1/sessions/outbox-check/search?q=Attached", "", 5, out, err);
            // Search shows a snippet, its lines joined and its end cut.
            CHECK(out.find("see the file Attached from Basecamp (on this machine): - /") != std::string::npos,
                  "the message does not name the file: %s", out.substr(0, 600).c_str());
        }
        request(addr, "DELETE", "/v1/sessions/outbox-check", "", 5, out, err);
        std::string rm = "rm -rf " + data;
        (void)!std::system(rm.c_str());
        unsetenv("XDG_DATA_HOME");
    }

    // Several machines asked at once: one that cannot be reached holds up
    // neither the others nor the caller.
    {
        Hub h;
        auto t0 = std::chrono::steady_clock::now();
        h.gather({"fd00::1", addr}, "/v1/usage");
        auto took = std::chrono::steady_clock::now() - t0;
        CHECK(took < std::chrono::milliseconds(200), "gather blocked the caller");
        std::string g;
        for (int i = 0; i < 100; i++) {
            g = h.gathered();
            if (g.find("\"rows\":") != std::string::npos) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
        size_t me = g.find("\"address\":\"" + addr + "\"");
        CHECK(me != std::string::npos && g.find("\"done\":true", me) != std::string::npos &&
              g.find("\"rows\":[", me) != std::string::npos, "the reachable one: %s", g.substr(0, 400).c_str());
        CHECK(g.find("{\"address\":\"fd00::1\",\"done\":false") != std::string::npos ||
              g.find("{\"address\":\"fd00::1\",\"done\":true,\"error\":\"\"") == std::string::npos,
              "the unreachable one answered: %s", g.substr(0, 400).c_str());
    }

    std::printf(fails ? "\n%d FAILED\n" : "\nall passed\n", fails);
    return fails ? 1 : 0;
}
