#include "shrooms_agents.h"

#include <arpa/inet.h>
#include <cctype>
#include <cerrno>
#include <chrono>
#include <fstream>
#include <functional>
#include <sstream>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <dirent.h>
#include <set>
#include <fcntl.h>
#include <netinet/in.h>
#include <poll.h>
#include <signal.h>
#include <spawn.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <sys/utsname.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>

extern "C" char** environ;

namespace agents {

// The environment for the programs this starts — xdg-open, the recorder, the
// clipboard tool — without what Basecamp's AppImage puts in its own: its
// LD_PRELOAD (libprocself_fix.so) makes /proc/self/exe name Basecamp's binary,
// and Ubuntu's coreutils, one multi-call binary, then refuse to run ("Security
// violation: Requested utility `cat` does not match executable name") — so
// xdg-open, a shell script, failed without a word and links did not open
// (2026-10-04). Its LD_LIBRARY_PATH and Qt plugin path would likewise hand
// its libraries to programs built against the system's.
ChildEnv childEnv()
{
    static const char* drop[] = {"LD_PRELOAD=", "LD_LIBRARY_PATH=", "QT_PLUGIN_PATH=", "QML2_IMPORT_PATH=",
                                 "QML_IMPORT_PATH=", "APPDIR=", "APPIMAGE=", "ARGV0=", "OWD=",
                                 // What libprocself_fix.so answers /proc/self/exe with.
                                 "__BUNDLE_REAL_EXE="};
    ChildEnv e;
    for (char** v = environ; v && *v; v++) {
        bool keep = true;
        for (const char* d : drop)
            if (std::strncmp(*v, d, std::strlen(d)) == 0) keep = false;
        if (keep) e.vars.emplace_back(*v);
    }
    for (auto& v : e.vars) e.ptrs.push_back(const_cast<char*>(v.c_str()));
    e.ptrs.push_back(nullptr);
    return e;
}

} // namespace agents

namespace agents {

namespace {

constexpr size_t kMaxBody = 8 * 1024 * 1024;
constexpr size_t kKeepEvents = 4000;
constexpr size_t kMaxReply = 512 * 1024;
// What is kept on disk of each conversation: as the phone keeps.
constexpr size_t kHistoryEvents = 300;
constexpr size_t kHistoryBytes = 1 << 20;
// Strings longer than this — a tool's output, nearly always, shown folded —
// are kept cut, as the phone keeps them: whole, a few filled the megabyte and a
// busy session's copy held 75 events.
constexpr size_t kHistoryString = 4096;

std::string jsonEscape(const std::string& s)
{
    std::string out;
    out.reserve(s.size() + 2);
    for (unsigned char c : s) {
        switch (c) {
        case '"': out += "\\\""; break;
        case '\\': out += "\\\\"; break;
        case '\n': out += "\\n"; break;
        case '\r': out += "\\r"; break;
        case '\t': out += "\\t"; break;
        default:
            if (c < 0x20) {
                char b[8];
                std::snprintf(b, sizeof b, "\\u%04x", c);
                out += b;
            } else {
                out += static_cast<char>(c);
            }
        }
    }
    return out;
}

// Opens a TCP connection to a mesh address, or returns -1 with why. Both
// timeouts are set, so a peer that went away cannot hang a caller.
//
// The connection is made in slices of 100 ms, at most connectSec (timeoutSec
// when 0) in all, asking `cancelled` between them. A blocking connect to a
// machine that does not answer took the whole send timeout — 60 s for a
// session being followed — and switching sessions waits for the follower:
// Basecamp froze, its call to the core timing out after 20 s (2026-10-04).
int dial(const std::string& address, int timeoutSec, std::string& err,
         const std::function<bool()>& cancelled = {}, int connectSec = 0)
{
    if (!isMeshAddress(address)) {
        err = address + " is not a mesh address";
        return -1;
    }
    sockaddr_storage ss{};
    socklen_t len = 0;
    int family = AF_INET6;
    auto* v6 = reinterpret_cast<sockaddr_in6*>(&ss);
    auto* v4 = reinterpret_cast<sockaddr_in*>(&ss);
    if (inet_pton(AF_INET6, address.c_str(), &v6->sin6_addr) == 1) {
        v6->sin6_family = AF_INET6;
        v6->sin6_port = htons(kPort);
        len = sizeof(sockaddr_in6);
    } else {
        family = AF_INET;
        inet_pton(AF_INET, address.c_str(), &v4->sin_addr);
        v4->sin_family = AF_INET;
        v4->sin_port = htons(kPort);
        len = sizeof(sockaddr_in);
    }
    int fd = ::socket(family, SOCK_STREAM, 0);
    if (fd < 0) {
        err = std::string("socket: ") + std::strerror(errno);
        return -1;
    }
    int flags = ::fcntl(fd, F_GETFL, 0);
    ::fcntl(fd, F_SETFL, flags | O_NONBLOCK);
    int rc = ::connect(fd, reinterpret_cast<sockaddr*>(&ss), len);
    if (rc < 0 && errno != EINPROGRESS) {
        err = "cannot reach " + address + ": " + std::strerror(errno);
        ::close(fd);
        return -1;
    }
    if (rc < 0) {
        int limitMs = (connectSec > 0 ? connectSec : timeoutSec) * 1000;
        int waited = 0;
        for (;;) {
            if (cancelled && cancelled()) {
                err = "cancelled";
                ::close(fd);
                return -1;
            }
            pollfd p{fd, POLLOUT, 0};
            int r = ::poll(&p, 1, 100);
            if (r > 0) break;
            if (r < 0 && errno != EINTR) {
                err = std::string("poll: ") + std::strerror(errno);
                ::close(fd);
                return -1;
            }
            waited += 100;
            if (waited >= limitMs) {
                err = "cannot reach " + address + ": " + std::strerror(ETIMEDOUT);
                ::close(fd);
                return -1;
            }
        }
        int soErr = 0;
        socklen_t sl = sizeof soErr;
        ::getsockopt(fd, SOL_SOCKET, SO_ERROR, &soErr, &sl);
        if (soErr != 0) {
            err = "cannot reach " + address + ": " + std::strerror(soErr);
            ::close(fd);
            return -1;
        }
    }
    ::fcntl(fd, F_SETFL, flags);
    timeval tv{};
    tv.tv_sec = timeoutSec;
    ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
    ::setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv);
    return fd;
}

bool sendAll(int fd, const std::string& data, std::string& err)
{
    size_t sent = 0;
    while (sent < data.size()) {
        ssize_t n = ::send(fd, data.data() + sent, data.size() - sent, MSG_NOSIGNAL);
        if (n <= 0) {
            err = std::string("write: ") + std::strerror(errno);
            return false;
        }
        sent += static_cast<size_t>(n);
    }
    return true;
}

std::string requestText(const std::string& address, const std::string& method,
                        const std::string& target, const std::string& body, bool stream)
{
    std::string host = address.find(':') != std::string::npos ? "[" + address + "]" : address;
    // HTTP/1.0, so Go never chunks the reply. Over 1.1 an event stream comes
    // chunked, and a chunk boundary can fall in the middle of an event line.
    std::string req = method + " " + target + " HTTP/1.0\r\nHost: " + host + ":" +
                      std::to_string(kPort) + "\r\nConnection: close\r\n";
    if (stream) {
        req += "Accept: text/event-stream\r\n";
    }
    if (method != "GET") {
        req += "Content-Type: application/json\r\nContent-Length: " + std::to_string(body.size()) + "\r\n";
    }
    return req + "\r\n" + body;
}

// The agent's own error text from a JSON {"error": "..."} body, or the body.
std::string errorText(const std::string& body)
{
    auto k = body.find("\"error\":\"");
    if (k == std::string::npos) {
        return body.substr(0, 300);
    }
    auto start = k + 9;
    auto end = body.find('"', start);
    return body.substr(start, end == std::string::npos ? std::string::npos : end - start);
}

// Removes HTTP/1.1 chunked transfer framing, which Go uses for any reply it
// did not know the length of.
std::string unchunk(const std::string& in)
{
    std::string out;
    size_t i = 0;
    while (i < in.size()) {
        auto eol = in.find("\r\n", i);
        if (eol == std::string::npos) break;
        long n = std::strtol(in.substr(i, eol - i).c_str(), nullptr, 16);
        if (n <= 0) break;
        out.append(in, eol + 2, static_cast<size_t>(n));
        i = eol + 2 + static_cast<size_t>(n) + 2;
    }
    return out;
}

long long seqOf(const std::string& ev)
{
    auto k = ev.find("\"seq\":");
    return k == std::string::npos ? 0 : std::atoll(ev.c_str() + k + 6);
}

}  // namespace

bool safePath(const std::string& path)
{
    if (path.compare(0, 4, "/v1/") != 0 || path.size() > 512) return false;
    for (unsigned char c : path) {
        if (c <= 0x20 || c >= 0x7f) return false;
    }
    return path.find("..") == std::string::npos;
}

bool makeDirs(const std::string& path, unsigned mode)
{
    if (path.empty()) return false;
    struct stat st {};
    if (::stat(path.c_str(), &st) == 0) return S_ISDIR(st.st_mode);
    size_t cut = path.find_last_of('/');
    if (cut != std::string::npos && cut > 0 && !makeDirs(path.substr(0, cut), mode)) return false;
    if (::mkdir(path.c_str(), static_cast<mode_t>(mode)) == 0) return true;
    return errno == EEXIST && ::stat(path.c_str(), &st) == 0 && S_ISDIR(st.st_mode);
}

bool isMeshAddress(const std::string& address)
{
    unsigned char b[16];
    if (inet_pton(AF_INET6, address.c_str(), b) == 1) {
        return b[0] == 0xfd;
    }
    if (inet_pton(AF_INET, address.c_str(), b) == 1) {
        return b[0] == 198 && (b[1] == 18 || b[1] == 19);
    }
    return false;
}

bool request(const std::string& address, const std::string& method, const std::string& target,
             const std::string& body, int timeoutSec, std::string& out, std::string& err)
{
    int fd = dial(address, timeoutSec, err);
    if (fd < 0) return false;
    if (!sendAll(fd, requestText(address, method, target, body, false), err)) {
        ::close(fd);
        return false;
    }
    std::string raw;
    char buf[8192];
    for (;;) {
        ssize_t n = ::recv(fd, buf, sizeof buf, 0);
        if (n < 0) {
            err = std::string("read: ") + std::strerror(errno);
            ::close(fd);
            return false;
        }
        if (n == 0) break;
        raw.append(buf, static_cast<size_t>(n));
        if (raw.size() > kMaxBody) {
            err = "the reply is implausibly large";
            ::close(fd);
            return false;
        }
    }
    ::close(fd);
    auto sep = raw.find("\r\n\r\n");
    if (sep == std::string::npos || raw.compare(0, 5, "HTTP/") != 0) {
        err = "the reply is not HTTP";
        return false;
    }
    std::string headers = raw.substr(0, sep);
    std::string payload = raw.substr(sep + 4);
    for (auto& c : headers) c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
    if (headers.find("transfer-encoding: chunked") != std::string::npos) {
        payload = unchunk(payload);
    }
    auto sp = raw.find(' ');
    std::string code = sp == std::string::npos ? "" : raw.substr(sp + 1, 3);
    if (code.empty() || code[0] != '2') {
        err = "the agent answered " + code + ": " + errorText(payload);
        return false;
    }
    out = payload;
    return true;
}

Hub::~Hub()
{
    stopFollower();
    // The sender holds `this`: it is stopped and waited for, not left
    // running into freed memory.
    stopping_ = true;
    if (sender_.joinable()) sender_.join();
    // A voice download in progress: its child killed, then waited for.
    int child = voiceChild_.load();
    if (child > 0) ::kill(child, SIGTERM);
    if (voiceThread_.joinable()) voiceThread_.join();
}

void Hub::find(const std::string& peers)
{
    bool expected = false;
    if (!finding_.compare_exchange_strong(expected, true)) return;

    std::vector<std::vector<std::string>> list;
    size_t i = 0;
    while (i <= peers.size()) {
        auto end = peers.find(';', i);
        std::string entry = peers.substr(i, end == std::string::npos ? std::string::npos : end - i);
        std::vector<std::string> f;
        size_t j = 0;
        while (f.size() < 3) {
            auto bar = entry.find('|', j);
            f.push_back(entry.substr(j, bar == std::string::npos ? std::string::npos : bar - j));
            if (bar == std::string::npos) break;
            j = bar + 1;
        }
        if (f.size() == 3 && isMeshAddress(f[2])) list.push_back(f);
        if (end == std::string::npos) break;
        i = end + 1;
    }

    std::thread([this, list]() {
        // One prober per peer, so a peer that does not answer costs its own
        // timeout and nobody else's.
        std::vector<std::thread> probes;
        for (const auto& p : list) {
            probes.emplace_back([this, p]() {
                std::string body, err;
                bool ok = request(p[2], "GET", "/v1/sessions", "", 3, body, err);
                std::lock_guard<std::mutex> g(mu_);
                if (ok) {
                    found_[p[2]] = "{\"name\":\"" + jsonEscape(p[0]) + "\",\"mesh\":\"" + jsonEscape(p[1]) +
                                   "\",\"address\":\"" + jsonEscape(p[2]) + "\",\"list\":" + body + "}";
                } else {
                    found_.erase(p[2]);
                }
            });
        }
        for (auto& t : probes) t.join();
        finding_ = false;
    }).detach();
}

std::string Hub::found()
{
    std::lock_guard<std::mutex> g(mu_);
    std::string out = "[";
    bool first = true;
    for (const auto& kv : found_) {
        if (!first) out += ",";
        out += kv.second;
        first = false;
    }
    return out + "]";
}

void Hub::stopFollower()
{
    generation_++;
    int fd = followFd_.exchange(-1);
    if (fd >= 0) ::shutdown(fd, SHUT_RDWR);
    if (follower_.joinable()) follower_.join();
}

// A session's last_seq as its machine last listed it (find), 0 when not
// known. With mu_ held.
long long Hub::listedLastSeq(const std::string& address, const std::string& session)
{
    auto it = found_.find(address);
    if (it == found_.end()) return 0;
    const std::string& j = it->second;
    size_t at = j.find("{\"name\":\"" + jsonEscape(session) + "\",\"dir\":");
    if (at == std::string::npos) return 0;
    size_t ls = j.find("\"last_seq\":", at);
    size_t next = j.find("{\"name\":\"", at + 1);
    if (ls == std::string::npos || (next != std::string::npos && ls > next)) return 0;
    return std::atoll(j.c_str() + ls + 11);
}

void Hub::watch(const std::string& address, const std::string& session, int tail)
{
    stopFollower();
    // What was kept of it, shown at once; then only what came after it is
    // asked for — usually nothing or a few events. Replaying the last `tail`
    // instead, tool output and all, took tens of seconds over the mesh, and
    // the conversation was rebuilt under the reader as it came. Only when
    // opening at the end, which is what was kept; a negative tail opens at
    // the end without the copy (it is of a session since made again).
    if (tail < 0) {
        tail = -tail;
        forgetHistory(address, session);
    }
    long long kept = 0;
    std::vector<std::string> keptEvents;
    if (tail > 0) {
        std::ifstream in(historyPath(address, session));
        std::string line;
        if (std::getline(in, line) && line.compare(0, 9, "{\"saved\":") == 0) {
            kept = std::atoll(line.c_str() + 9);
            while (std::getline(in, line)) {
                if (!line.empty()) keptEvents.push_back(line);
            }
        }
    }
    long long from = 0;
    {
        std::lock_guard<std::mutex> g(mu_);
        base_ += static_cast<long long>(events_.size());
        events_.clear();
        connected_ = false;
        error_.clear();
        kept_ = 0;
        keptLast_ = 0;
        // A copy far behind — the session went on elsewhere, from the phone,
        // while nothing here watched it — is not caught up from: that is a
        // replay of every event since, megabytes of tool output, and the
        // conversation rebuilt under the reader piece by piece (2026-10-06).
        // Opened at the end instead, as without a copy. Judged against the
        // session's newest event as last listed, when it was.
        if (kept > 0 && !keptEvents.empty() && tail > 0) {
            long long listed = listedLastSeq(address, session); // mu_ is held
            if (listed > 0 && listed - seqOf(keptEvents.back()) > tail) {
                kept = 0;
                keptEvents.clear();
            }
        }
        if (kept > 0 && !keptEvents.empty()) {
            keptLast_ = seqOf(keptEvents.back());
            events_ = std::move(keptEvents);
            kept_ = kept;
        }
        from = keptLast_;
    }
    unsigned gen = generation_.load();
    follower_ = std::thread(&Hub::follow, this, address, session, tail, from, gen);
}

void Hub::follow(std::string address, std::string session, int tail, long long after, unsigned generation)
{
    bool dirty = false;
    // The first events are kept at once; after that, at most every 5 seconds.
    std::chrono::steady_clock::time_point lastSave{};
    while (generation_.load() == generation) {
        std::string err;
        // A long read timeout: the agent sends a comment every 20 seconds, so
        // 60 without anything is a dead connection, the normal way they end.
        int fd = dial(address, 60, err, [&] { return generation_.load() != generation; }, 10);
        if (fd >= 0) {
            followFd_ = fd;
            std::string target = "/v1/sessions/" + session + "/events?after=" + std::to_string(after);
            // Only on the first connection: a reconnect is catching up.
            if (after == 0 && tail > 0) target += "&tail=" + std::to_string(tail);
            if (sendAll(fd, requestText(address, "GET", target, "", true), err)) {
                std::string buf;
                bool inBody = false;
                char chunk[8192];
                for (;;) {
                    ssize_t n = ::recv(fd, chunk, sizeof chunk, 0);
                    if (n <= 0) break;
                    buf.append(chunk, static_cast<size_t>(n));
                    if (!inBody) {
                        auto sep = buf.find("\r\n\r\n");
                        if (sep == std::string::npos) continue;
                        if (buf.compare(0, 5, "HTTP/") != 0 || buf.compare(8, 4, " 200") != 0) {
                            err = "the agent refused the stream: " + buf.substr(0, buf.find("\r\n"));
                            break;
                        }
                        buf.erase(0, sep + 4);
                        inBody = true;
                        std::lock_guard<std::mutex> g(mu_);
                        connected_ = true;
                        error_.clear();
                        // The machine answered: what follows is its own.
                        kept_ = 0;
                    }
                    // Lines of the stream. Chunk framing, when present, sits on
                    // lines of its own and is skipped as not being data.
                    size_t eol;
                    while ((eol = buf.find('\n')) != std::string::npos) {
                        std::string line = buf.substr(0, eol);
                        buf.erase(0, eol + 1);
                        if (!line.empty() && line.back() == '\r') line.pop_back();
                        if (line.compare(0, 6, "data: ") != 0) continue;
                        std::string ev = line.substr(6);
                        bool partial = ev.find("\"kind\":\"partial\"") != std::string::npos;
                        if (!partial) {
                            long long s = seqOf(ev);
                            if (s <= after) continue;
                            after = s;
                        }
                        std::lock_guard<std::mutex> g(mu_);
                        events_.push_back(ev);
                        if (events_.size() > kKeepEvents) {
                            size_t drop = events_.size() - kKeepEvents;
                            events_.erase(events_.begin(), events_.begin() + static_cast<long>(drop));
                            base_ += static_cast<long long>(drop);
                        }
                        if (!partial) dirty = true;
                    }
                    if (dirty && std::chrono::steady_clock::now() - lastSave > std::chrono::seconds(5)) {
                        saveHistory(address, session);
                        dirty = false;
                        lastSave = std::chrono::steady_clock::now();
                    }
                }
            }
            followFd_ = -1;
            ::close(fd);
        }
        {
            std::lock_guard<std::mutex> g(mu_);
            connected_ = false;
            if (!err.empty()) error_ = err;
        }
        if (dirty) {
            saveHistory(address, session);
            dirty = false;
            lastSave = std::chrono::steady_clock::now();
        }
        for (int i = 0; i < 20 && generation_.load() == generation; i++) {
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
    }
}

void Hub::forgetHistory(const std::string& address, const std::string& session)
{
    std::remove(historyPath(address, session).c_str());
}

std::string Hub::historyPath(const std::string& address, const std::string& session)
{
    // FNV-1a: a file name for the pair, not a secret.
    unsigned long long h = 14695981039346656037ULL;
    for (unsigned char c : address + "/" + session) {
        h ^= c;
        h *= 1099511628211ULL;
    }
    char name[32];
    std::snprintf(name, sizeof name, "%016llx.jsonl", h);
    return dataDir("history") + "/" + name;
}

// ev, a JSON event, with every string value longer than max bytes cut to
// about max and ended with "…". Cut between characters: never inside an
// escape or a UTF-8 sequence, so what comes out is still valid JSON.
std::string trimStrings(const std::string& ev, size_t max)
{
    std::string out;
    out.reserve(ev.size() < 2 * max ? ev.size() : 2 * max);
    size_t i = 0, n = ev.size();
    while (i < n) {
        char c = ev[i];
        out += c;
        i++;
        if (c != '"') continue;
        // Inside a string: copy up to max bytes, then skip to its end.
        size_t kept = 0;
        bool cut = false;
        while (i < n && ev[i] != '"') {
            size_t len = 1;
            if (ev[i] == '\\') len = (i + 1 < n && ev[i + 1] == 'u') ? 6 : 2;
            else if ((static_cast<unsigned char>(ev[i]) & 0xE0) == 0xC0) len = 2;
            else if ((static_cast<unsigned char>(ev[i]) & 0xF0) == 0xE0) len = 3;
            else if ((static_cast<unsigned char>(ev[i]) & 0xF8) == 0xF0) len = 4;
            if (i + len > n) len = n - i;
            if (!cut && kept + len > max) {
                cut = true;
                out += "\u2026";
            }
            if (!cut) {
                out.append(ev, i, len);
                kept += len;
            }
            i += len;
        }
        if (i < n) {
            out += '"';
            i++;
        }
    }
    return out;
}

// The newest events that fit in kHistoryEvents and kHistoryBytes, streamed
// text left out, after a first line saying when: {"saved":MS}.
void Hub::saveHistory(const std::string& address, const std::string& session)
{
    std::vector<std::string> keep;
    std::string out;
    {
        std::lock_guard<std::mutex> g(mu_);
        if (kept_ != 0 || events_.empty()) return;
        size_t bytes = 0;
        for (auto it = events_.rbegin(); it != events_.rend() && keep.size() < kHistoryEvents; ++it) {
            if (it->find("\"kind\":\"partial\"") != std::string::npos) continue;
            std::string cut = trimStrings(*it, kHistoryString);
            if (bytes + cut.size() > kHistoryBytes) break;
            bytes += cut.size() + 1;
            keep.push_back(std::move(cut));
        }
        long long now = std::chrono::duration_cast<std::chrono::milliseconds>(
                            std::chrono::system_clock::now().time_since_epoch()).count();
        out = "{\"saved\":" + std::to_string(now) + "}\n";
        out.reserve(out.size() + bytes);
        for (auto it = keep.rbegin(); it != keep.rend(); ++it) out += *it + "\n";
    }
    std::string path = historyPath(address, session), tmp = path + ".tmp";
    {
        std::ofstream f(tmp, std::ios::trunc);
        f << out;
        if (!f) return;
    }
    std::rename(tmp.c_str(), path.c_str());
}

std::string Hub::events(long long after)
{
    std::lock_guard<std::mutex> g(mu_);
    long long from = after < base_ ? base_ : after;
    // At most about kMaxReply per answer, and "more" when there is more: a
    // long session's backlog is megabytes of tool output, and one reply that
    // size through Basecamp's IPC is the likely reason conversations showed
    // empty after switching to them (2026-10-03; not proven).
    std::string body;
    long long i = from - base_;
    for (; i < static_cast<long long>(events_.size()); i++) {
        const std::string& ev = events_[static_cast<size_t>(i)];
        if (!body.empty() && body.size() + ev.size() > kMaxReply) break;
        if (!body.empty()) body += ",";
        body += ev;
    }
    bool more = i < static_cast<long long>(events_.size());
    return "{\"next\":" + std::to_string(base_ + i) + ",\"more\":" + (more ? "true" : "false") +
           ",\"connected\":" + (connected_ ? "true" : "false") +
           ",\"kept\":" + std::to_string(kept_) + ",\"epoch\":" + std::to_string(epoch_) +
           ",\"error\":\"" + jsonEscape(error_) + "\",\"events\":[" + body + "]}";
}

namespace {

constexpr long long kMaxUpload = 50LL * 1024 * 1024;

std::string urlEncode(const std::string& s)
{
    static const char* hex = "0123456789ABCDEF";
    std::string out;
    for (unsigned char c : s) {
        if (std::isalnum(c) || c == '-' || c == '_' || c == '.' || c == '~') {
            out += static_cast<char>(c);
        } else {
            out += '%';
            out += hex[c >> 4];
            out += hex[c & 15];
        }
    }
    return out;
}

std::string baseName(const std::string& path)
{
    auto k = path.find_last_of('/');
    return k == std::string::npos ? path : path.substr(k + 1);
}

bool readFile(const std::string& path, std::string& out, std::string& err)
{
    struct stat st{};
    if (::stat(path.c_str(), &st) != 0 || !S_ISREG(st.st_mode)) {
        err = path + " is not a file";
        return false;
    }
    if (st.st_size > kMaxUpload) {
        err = "the file is larger than 50 MB";
        return false;
    }
    std::ifstream f(path, std::ios::binary);
    std::stringstream ss;
    ss << f.rdbuf();
    out = ss.str();
    if (out.empty()) {
        err = "the file is empty";
        return false;
    }
    return true;
}

}  // namespace

// A string field of a small JSON reply; enough for {"path":..,"text":..}.
std::string field(const std::string& json, const std::string& key)
{
    auto k = json.find("\"" + key + "\":\"");
    if (k == std::string::npos) return "";
    std::string out;
    for (size_t i = k + key.size() + 4; i < json.size(); i++) {
        char c = json[i];
        if (c == '"') break;
        if (c == '\\' && i + 1 < json.size()) {
            char n = json[++i];
            switch (n) {
            case 'n': out += '\n'; break;
            case 't': out += '\t'; break;
            case 'u': {
                // Go escapes <, > and & this way, and control characters:
                // decoded, so "A & B" comes back as said. A surrogate pair is
                // not combined; Go sends non-BMP text as UTF-8, not escaped.
                if (i + 4 >= json.size()) break;
                unsigned cp = static_cast<unsigned>(std::strtoul(json.substr(i + 1, 4).c_str(), nullptr, 16));
                i += 4;
                if (cp < 0x80) {
                    out += static_cast<char>(cp);
                } else if (cp < 0x800) {
                    out += static_cast<char>(0xC0 | (cp >> 6));
                    out += static_cast<char>(0x80 | (cp & 0x3F));
                } else {
                    out += static_cast<char>(0xE0 | (cp >> 12));
                    out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
                    out += static_cast<char>(0x80 | (cp & 0x3F));
                }
                break;
            }
            default: out += n;
            }
        } else {
            out += c;
        }
    }
    return out;
}

long Hub::addJob(const std::string& kind, const std::string& name)
{
    std::lock_guard<std::mutex> g(mu_);
    Job j{nextJob_++, kind, "pending", name, "", "", ""};
    jobs_.push_back(j);
    if (jobs_.size() > 50) jobs_.erase(jobs_.begin());
    return j.id;
}

void Hub::finishJob(long id, bool ok, const std::string& path, const std::string& text, const std::string& error)
{
    std::lock_guard<std::mutex> g(mu_);
    for (auto& j : jobs_) {
        if (j.id != id) continue;
        j.state = ok ? "done" : "failed";
        j.path = path;
        j.text = text;
        j.error = error;
    }
}

long Hub::upload(const std::string& address, const std::string& session, const std::string& localPath)
{
    std::string name = baseName(localPath);
    long id = addJob("upload", name);
    std::thread([this, id, address, session, localPath, name]() {
        std::string body, out, err;
        if (!readFile(localPath, body, err)) {
            finishJob(id, false, "", "", err);
            return;
        }
        bool ok = request(address, "POST", "/v1/sessions/" + session + "/files?name=" + urlEncode(name),
                          body, 120, out, err);
        finishJob(id, ok, ok ? field(out, "path") : "", "", err);
    }).detach();
    return id;
}

namespace {

bool executable(const std::string& p) { return ::access(p.c_str(), X_OK) == 0; }

// The sample rate a Piper voice speaks at, from its .onnx.json; 22050, the
// usual, when it does not say.
int piperRate(const std::string& model)
{
    std::ifstream in(model + ".json");
    std::stringstream ss;
    ss << in.rdbuf();
    std::string j = ss.str();
    auto k = j.find("\"sample_rate\"");
    if (k == std::string::npos) return 22050;
    auto c = j.find(':', k);
    int r = c == std::string::npos ? 0 : std::atoi(j.c_str() + c + 1);
    return r > 0 ? r : 22050;
}

std::string shellQuote(const std::string& s)
{
    std::string out = "'";
    for (char c : s) {
        if (c == '\'') out += "'\\''";
        else out += c;
    }
    return out + "'";
}

}  // namespace

std::string Hub::speak(const std::string& text, bool czech)
{
    speakStop();
    std::string piperDir = dataDir("piper");
    std::string model = piperDir + (czech ? "/cs.onnx" : "/en.onnx");
    // Set up by voiceSetup, the official build unpacks into piper/ beside its
    // libraries; a piper binary straight in the folder is the hand-made way.
    std::string piper = executable(piperDir + "/piper/piper") ? piperDir + "/piper/piper" : piperDir + "/piper";
    std::vector<std::string> argv;
    std::string engine;
    char tmpl[] = "/tmp/shrooms-say-XXXXXX";
    int fd = ::mkstemp(tmpl);
    if (fd < 0) return std::string("mkstemp: ") + std::strerror(errno);
    ssize_t w = ::write(fd, text.data(), text.size());
    ::close(fd);
    if (w != static_cast<ssize_t>(text.size())) {
        ::unlink(tmpl);
        return "could not write the text";
    }
    std::string file = tmpl;
    if (executable(piper) && ::access(model.c_str(), R_OK) == 0) {
        // Raw audio straight to the sound server: pw-play, else aplay.
        std::string rate = std::to_string(piperRate(model));
        std::string cmd = shellQuote(piper) + " --model " + shellQuote(model) + " --output-raw < " + shellQuote(file) +
            " 2>/dev/null | { pw-play --rate " + rate + " --channels 1 --format s16 - 2>/dev/null || aplay -q -r " + rate +
            " -f S16_LE -c 1 -t raw - ; }; rm -f " + shellQuote(file);
        argv = {"/bin/sh", "-c", cmd};
        engine = "piper";
    } else {
        // -w: the client lives as long as the speech, so its exit says done.
        std::string cmd = "spd-say -w -l " + std::string(czech ? "cs" : "en") + " -- \"$(cat " + shellQuote(file) + ")\"; rm -f " +
            shellQuote(file);
        argv = {"/bin/sh", "-c", cmd};
        engine = "spd-say";
    }
    posix_spawnattr_t at;
    posix_spawnattr_init(&at);
    posix_spawnattr_setflags(&at, POSIX_SPAWN_SETPGROUP);
    posix_spawnattr_setpgroup(&at, 0);
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_addopen(&fa, 1, "/dev/null", O_WRONLY, 0);
    posix_spawn_file_actions_addopen(&fa, 2, "/dev/null", O_WRONLY, 0);
    std::vector<char*> av;
    for (auto& a : argv) av.push_back(const_cast<char*>(a.c_str()));
    av.push_back(nullptr);
    pid_t pid;
    int rc = posix_spawn(&pid, av[0], &fa, &at, av.data(), childEnv().ptrs.data());
    posix_spawn_file_actions_destroy(&fa);
    posix_spawnattr_destroy(&at);
    if (rc != 0) {
        ::unlink(file.c_str());
        return std::string("cannot start ") + engine + ": " + std::strerror(rc);
    }
    std::lock_guard<std::mutex> g(mu_);
    speaker_ = pid;
    speakEngine_ = engine;
    return "";
}

// Runs a program and waits for it, killable from ~Hub; its exit status, or
// -1 when it could not be started or was stopped.
int Hub::run(const std::vector<std::string>& args)
{
    if (stopping_) return -1;
    std::vector<char*> av;
    for (auto& a : args) av.push_back(const_cast<char*>(a.c_str()));
    av.push_back(nullptr);
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_addopen(&fa, 1, "/dev/null", O_WRONLY, 0);
    posix_spawn_file_actions_addopen(&fa, 2, "/dev/null", O_WRONLY, 0);
    pid_t pid;
    int rc = posix_spawnp(&pid, av[0], &fa, nullptr, av.data(), childEnv().ptrs.data());
    posix_spawn_file_actions_destroy(&fa);
    if (rc != 0) return -1;
    voiceChild_ = pid;
    int status = 0;
    ::waitpid(pid, &status, 0);
    voiceChild_ = -1;
    if (stopping_) return -1;
    return WIFEXITED(status) ? WEXITSTATUS(status) : -1;
}

namespace {

// What voiceSetup fetches. The voice from rhasspy's own repository: the copy
// repackaged for sherpa-onnx ran at half real time under this build of Piper,
// the original at twelve times (2026-10-05, this laptop).
const char* kPiperRelease = "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_";
const char* kVoiceBase = "https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium.onnx";

std::string machineArch()
{
    struct utsname u {};
    if (::uname(&u) != 0) return "";
    std::string m = u.machine;
    if (m == "x86_64" || m == "amd64") return "x86_64";
    if (m == "aarch64" || m == "arm64") return "aarch64";
    return "";
}

}  // namespace

void Hub::voiceSetup()
{
    bool expected = false;
    if (!voiceBusy_.compare_exchange_strong(expected, true)) return;
    if (voiceThread_.joinable()) voiceThread_.join();
    {
        std::lock_guard<std::mutex> g(mu_);
        voiceError_.clear();
        voiceStep_ = "starting";
    }
    voiceThread_ = std::thread([this]() {
        auto step = [this](const std::string& s) { std::lock_guard<std::mutex> g(mu_); voiceStep_ = s; };
        auto fail = [this](const std::string& why) {
            std::lock_guard<std::mutex> g(mu_);
            voiceError_ = why;
            voiceStep_.clear();
        };
        std::string arch = machineArch();
        std::string dir = dataDir("piper"), stage = dir + "/.setup";
        run({"rm", "-rf", stage});
        makeDirs(stage);
        if (arch.empty()) {
            fail("Piper has no build for this machine's architecture");
        } else if (step("downloading Piper (25 MB)"),
                   run({"curl", "-fsSL", "--retry", "2", "-o", stage + "/piper.tar.gz", std::string(kPiperRelease) + arch + ".tar.gz"}) != 0) {
            fail(stopping_ ? "stopped" : "could not download Piper (is curl installed, and the network up?)");
        } else if (step("unpacking Piper"), run({"tar", "-xzf", stage + "/piper.tar.gz", "-C", stage}) != 0) {
            fail("could not unpack Piper");
        } else if (step("downloading the voice (63 MB)"),
                   run({"curl", "-fsSL", "--retry", "2", "-o", stage + "/en.onnx", kVoiceBase}) != 0 ||
                       run({"curl", "-fsSL", "--retry", "2", "-o", stage + "/en.onnx.json", std::string(kVoiceBase) + ".json"}) != 0) {
            fail(stopping_ ? "stopped" : "could not download the voice");
        } else {
            // Moved into place only when all of it is here: a half-done setup
            // is never taken for a working one.
            step("installing");
            run({"rm", "-rf", dir + "/piper"});
            bool ok = std::rename((stage + "/piper").c_str(), (dir + "/piper").c_str()) == 0 &&
                      std::rename((stage + "/en.onnx").c_str(), (dir + "/en.onnx").c_str()) == 0 &&
                      std::rename((stage + "/en.onnx.json").c_str(), (dir + "/en.onnx.json").c_str()) == 0;
            if (!ok) fail(std::string("could not install it: ") + std::strerror(errno));
            else step("");
        }
        run({"rm", "-rf", stage});
        voiceBusy_ = false;
    });
}

void Hub::voiceRemove()
{
    if (voiceBusy_) return;
    speakStop();
    std::string dir = dataDir("piper");
    run({"rm", "-rf", dir + "/piper", dir + "/en.onnx", dir + "/en.onnx.json"});
}

std::string Hub::voiceState()
{
    std::string dir = dataDir("piper");
    bool installed = (executable(dir + "/piper/piper") || executable(dir + "/piper")) && ::access((dir + "/en.onnx").c_str(), R_OK) == 0;
    bool spd = false;
    for (const char* p : {"/usr/bin/spd-say", "/usr/local/bin/spd-say", "/bin/spd-say"}) spd = spd || executable(p);
    std::lock_guard<std::mutex> g(mu_);
    return std::string("{\"installed\":") + (installed ? "true" : "false") + ",\"busy\":" + (voiceBusy_ ? "true" : "false") +
           ",\"step\":\"" + jsonEscape(voiceStep_) + "\",\"error\":\"" + jsonEscape(voiceError_) + "\",\"engine\":\"" +
           (installed ? "piper" : (spd ? "spd-say" : "none")) + "\",\"voice\":\"en_US-lessac-medium\"}";
}

void Hub::speakStop()
{
    int pid;
    std::string engine;
    {
        std::lock_guard<std::mutex> g(mu_);
        pid = speaker_;
        engine = speakEngine_;
        speaker_ = -1;
    }
    if (pid <= 0) return;
    ::kill(-pid, SIGTERM);
    ::waitpid(pid, nullptr, 0);
    // speech-dispatcher goes on with what it was given; tell it to stop.
    if (engine == "spd-say") {
        const char* av[] = {"spd-say", "-C", nullptr};
        pid_t p;
        if (posix_spawnp(&p, av[0], nullptr, nullptr, const_cast<char**>(av), childEnv().ptrs.data()) == 0)
            ::waitpid(p, nullptr, 0);
    }
}

bool Hub::speaking(std::string& engine)
{
    std::lock_guard<std::mutex> g(mu_);
    engine = speakEngine_;
    if (speaker_ <= 0) return false;
    if (::waitpid(speaker_, nullptr, WNOHANG) == speaker_) {
        speaker_ = -1;
        return false;
    }
    return true;
}

namespace {
std::string slurp(const std::string& path);
}  // namespace

// What a recording that came out empty says: which recorder, and its own
// last words (kept beside the recording), so "the file is empty" on a new
// machine says whether there is no microphone, no permission or no daemon.
std::string emptyRecordingWhy(const std::string& recorder, const std::string& path)
{
    std::string said = slurp(path + ".err");
    while (!said.empty() && (said.back() == '\n' || said.back() == ' ')) said.pop_back();
    if (said.size() > 300) said = said.substr(said.size() - 300);
    return "nothing was recorded (" + (recorder.empty() ? std::string("the recorder") : recorder) + ")" +
           (said.empty() ? ": is there a microphone, and is it the default input?" : ": " + said);
}

// A WAV with nothing after its 44-byte header is as empty as no file.
bool recordedSomething(const std::string& path)
{
    struct stat st {};
    return ::stat(path.c_str(), &st) == 0 && st.st_size > 44;
}

std::string Hub::recordStart()
{
    std::lock_guard<std::mutex> g(mu_);
    if (recorder_ > 0) return "already recording";
    char tmpl[] = "/tmp/shrooms-voice-XXXXXX";
    int fd = ::mkstemp(tmpl);
    if (fd < 0) return std::string("mkstemp: ") + std::strerror(errno);
    ::close(fd);
    std::string path = std::string(tmpl) + ".wav";
    ::rename(tmpl, path.c_str());

    // Its errors beside the recording, not thrown away: a recorder that
    // starts and hears nothing is otherwise silent until the empty file is
    // sent (the Duet, 2026-10-06: "the file is empty").
    std::string errPath = path + ".err";
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_addopen(&fa, 1, "/dev/null", O_WRONLY, 0);
    posix_spawn_file_actions_addopen(&fa, 2, errPath.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0600);
    // Speech for a model that resamples to 16 kHz mono anyway.
    std::vector<std::vector<std::string>> tries = {
        {"pw-record", "--rate", "16000", "--channels", "1", path},
        {"parecord", "--file-format=wav", "--rate=16000", "--channels=1", path},
        {"arecord", "-q", "-f", "S16_LE", "-r", "16000", "-c", "1", path},
    };
    std::string why = "no recorder found (pw-record, parecord, arecord)";
    for (auto& t : tries) {
        std::vector<char*> argv;
        for (auto& a : t) argv.push_back(const_cast<char*>(a.c_str()));
        argv.push_back(nullptr);
        pid_t pid;
        int rc = posix_spawnp(&pid, argv[0], &fa, nullptr, argv.data(), childEnv().ptrs.data());
        if (rc != 0) {
            why = std::string(argv[0]) + ": " + std::strerror(rc);
            continue;
        }
        // One that ends at once — no PipeWire, no PulseAudio, no device — is
        // not recording: the next one may be (arecord, straight to ALSA).
        std::this_thread::sleep_for(std::chrono::milliseconds(300));
        if (::waitpid(pid, nullptr, WNOHANG) == pid) {
            std::string said = slurp(errPath);
            while (!said.empty() && (said.back() == '\n' || said.back() == ' ')) said.pop_back();
            why = std::string(argv[0]) + " stopped at once" + (said.empty() ? "" : ": " + said.substr(0, 200));
            continue;
        }
        recorder_ = pid;
        recording_ = path;
        recorderName_ = argv[0];
        posix_spawn_file_actions_destroy(&fa);
        return "";
    }
    posix_spawn_file_actions_destroy(&fa);
    ::unlink(path.c_str());
    ::unlink(errPath.c_str());
    return why;
}

namespace {

// Stops a recorder the way it expects, so it finishes the file's header, and
// does not wait forever for it.
void stopRecorder(int pid)
{
    ::kill(pid, SIGINT);
    for (int i = 0; i < 30; i++) {
        if (::waitpid(pid, nullptr, WNOHANG) == pid) return;
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    ::kill(pid, SIGKILL);
    ::waitpid(pid, nullptr, 0);
}

}  // namespace

void Hub::recordCancel()
{
    int pid;
    std::string path;
    {
        std::lock_guard<std::mutex> g(mu_);
        pid = recorder_;
        path = recording_;
        recorder_ = -1;
        recording_.clear();
    }
    if (pid > 0) stopRecorder(pid);
    if (!path.empty()) {
        ::unlink(path.c_str());
        ::unlink((path + ".err").c_str());
    }
}

long Hub::recordStop(const std::string& address, const std::string& session, const std::string& lang,
                     std::string& err)
{
    int pid;
    std::string path, recorder;
    {
        std::lock_guard<std::mutex> g(mu_);
        pid = recorder_;
        path = recording_;
        recorder = recorderName_;
        recorder_ = -1;
        recording_.clear();
    }
    if (pid <= 0) {
        err = "not recording";
        return -1;
    }
    long id = addJob("voice", "voice note");
    std::thread([this, id, pid, path, recorder, address, session, lang]() {
        stopRecorder(pid);
        std::string body, out, err;
        bool ok = recordedSomething(path);
        if (!ok) err = emptyRecordingWhy(recorder, path);
        else ok = readFile(path, body, err);
        ::unlink(path.c_str());
        ::unlink((path + ".err").c_str());
        if (ok) {
            // A transcription takes a while on a laptop CPU: a minute of
            // speech is most of one.
            ok = request(address, "POST", "/v1/sessions/" + session + "/transcribe?name=voice.wav&lang=" +
                         urlEncode(lang), body, 300, out, err);
        }
        finishJob(id, ok, ok ? field(out, "path") : "", ok ? field(out, "text") : "", err);
    }).detach();
    return id;
}

bool safeUrl(const std::string& url)
{
    if (url.size() > 4096) return false;
    if (url.rfind("http://", 0) != 0 && url.rfind("https://", 0) != 0) return false;
    for (unsigned char c : url)
        if (c <= 0x20 || c == 0x7f) return false; // spaces and control characters
    return true;
}

bool Hub::openUrl(const std::string& url, std::string& err)
{
    if (!safeUrl(url)) {
        err = "only http and https links are opened";
        return false;
    }
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_addopen(&fa, 1, "/dev/null", O_WRONLY, 0);
    posix_spawn_file_actions_addopen(&fa, 2, "/dev/null", O_WRONLY, 0);
    std::string bin = "xdg-open";
    char* argv[] = {const_cast<char*>(bin.c_str()), const_cast<char*>(url.c_str()), nullptr};
    pid_t pid;
    int rc = posix_spawnp(&pid, argv[0], &fa, nullptr, argv, childEnv().ptrs.data());
    posix_spawn_file_actions_destroy(&fa);
    if (rc != 0) {
        err = std::string("xdg-open: ") + std::strerror(rc);
        return false;
    }
    // xdg-open hands the link to the browser and exits; reaped here so it
    // does not linger as a zombie of Basecamp's.
    std::thread([pid]() { ::waitpid(pid, nullptr, 0); }).detach();
    return true;
}

namespace {

// Runs a command with its stdout to a file, waiting at most `secs`. Returns
// the exit status, or -1 if it could not be started (not installed).
int runTo(const std::vector<std::string>& cmd, const std::string& outPath, int secs)
{
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_addopen(&fa, 1, outPath.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0600);
    posix_spawn_file_actions_addopen(&fa, 2, "/dev/null", O_WRONLY, 0);
    std::vector<char*> argv;
    for (auto& a : cmd) argv.push_back(const_cast<char*>(a.c_str()));
    argv.push_back(nullptr);
    pid_t pid;
    int rc = posix_spawnp(&pid, argv[0], &fa, nullptr, argv.data(), childEnv().ptrs.data());
    posix_spawn_file_actions_destroy(&fa);
    if (rc != 0) return -1;
    int status = 0;
    for (int i = 0; i < secs * 20; i++) {
        if (::waitpid(pid, &status, WNOHANG) == pid) return WIFEXITED(status) ? WEXITSTATUS(status) : 1;
        std::this_thread::sleep_for(std::chrono::milliseconds(50));
    }
    ::kill(pid, SIGKILL);
    ::waitpid(pid, nullptr, 0);
    return 1;
}

std::string slurp(const std::string& path)
{
    std::ifstream f(path, std::ios::binary);
    std::stringstream ss;
    ss << f.rdbuf();
    return ss.str();
}

}  // namespace

namespace {
std::string newId();
}  // namespace

std::string Hub::pasteImage(std::string& err)
{
    char tmpl[] = "/tmp/shrooms-paste-XXXXXX";
    int fd = ::mkstemp(tmpl);
    if (fd < 0) {
        err = std::string("mkstemp: ") + std::strerror(errno);
        return "";
    }
    ::close(fd);
    std::string types = std::string(tmpl) + ".types";
    ::unlink(tmpl);

    // Which tool, and whether the clipboard holds an image at all: asked
    // first, so text is left to the ordinary paste.
    bool wayland = std::getenv("WAYLAND_DISPLAY") != nullptr;
    std::vector<std::string> list = wayland ? std::vector<std::string>{"wl-paste", "--list-types"}
                                            : std::vector<std::string>{"xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"};
    int rc = runTo(list, types, 3);
    std::string have = slurp(types);
    ::unlink(types.c_str());
    if (rc == -1) {
        err = wayland ? "pasting images needs wl-paste: sudo apt install wl-clipboard"
                      : "pasting images needs xclip: sudo apt install xclip";
        return "";
    }
    std::string mime;
    for (const char* m : {"image/png", "image/jpeg", "image/webp", "image/gif"}) {
        if (have.find(m) != std::string::npos) {
            mime = m;
            break;
        }
    }
    if (mime.empty()) return "";

    std::vector<std::string> get = wayland ? std::vector<std::string>{"wl-paste", "--type", mime}
                                           : std::vector<std::string>{"xclip", "-selection", "clipboard", "-t", mime, "-o"};
    std::string ext = mime.substr(6);
    std::string file = std::string(tmpl) + "." + ext;
    if (runTo(get, file, 5) != 0) {
        ::unlink(file.c_str());
        err = "could not read the image from the clipboard";
        return "";
    }
    std::string kept = outboxDir() + "/" + newId() + "-pasted." + ext;
    if (std::rename(file.c_str(), kept.c_str()) != 0) {
        std::string data;
        if (!readFile(file, data, err)) {
            ::unlink(file.c_str());
            return "";
        }
        std::ofstream(kept, std::ios::binary) << data;
        ::unlink(file.c_str());
    }
    return kept;
}

long Hub::search(const std::string& address, const std::string& session, const std::string& query)
{
    long id;
    {
        std::lock_guard<std::mutex> g(mu_);
        id = ++searchId_;
        searchDone_ = false;
        searchFound_ = "null";
        searchError_.clear();
    }
    std::thread([this, id, address, session, query]() {
        std::string out, err;
        bool ok = request(address, "GET", "/v1/sessions/" + session + "/search?limit=100&q=" + urlEncode(query),
                          "", 60, out, err);
        // Only the agent's {"found":[…]} is passed on, as it is.
        std::string found = "null";
        if (ok) {
            size_t at = out.find('[');
            size_t end = out.rfind(']');
            if (out.compare(0, 9, "{\"found\":") == 0 && at != std::string::npos && end != std::string::npos && end > at)
                found = out.substr(at, end - at + 1);
            else
                err = "the agent answered something else";
        }
        std::lock_guard<std::mutex> g(mu_);
        if (id != searchId_) return; // a newer search replaced it
        searchDone_ = true;
        searchFound_ = found;
        searchError_ = (ok && found != "null") ? "" : (err.empty() ? "no answer" : err);
    }).detach();
    return id;
}

std::string Hub::searched()
{
    std::lock_guard<std::mutex> g(mu_);
    return "{\"id\":" + std::to_string(searchId_) + ",\"done\":" + (searchDone_ ? "true" : "false") +
           ",\"error\":\"" + jsonEscape(searchError_) + "\",\"found\":" + searchFound_ + "}";
}

long Hub::gather(const std::vector<std::string>& addresses, const std::string& path)
{
    long id;
    {
        std::lock_guard<std::mutex> g(mu_);
        id = ++gatherId_;
        gathered_.clear();
        for (const auto& a : addresses) gathered_.push_back({a, "", "", false});
    }
    for (size_t i = 0; i < addresses.size(); i++) {
        std::thread([this, id, i, address = addresses[i], path]() {
            std::string out, err;
            // Long: a machine's first count of its usage reads every log it has.
            bool ok = request(address, "GET", path, "", 30, out, err);
            size_t at = out.find_first_not_of(" \t\r\n");
            if (ok && (at == std::string::npos || (out[at] != '{' && out[at] != '['))) {
                ok = false;
                err = "the agent answered something else";
            }
            std::lock_guard<std::mutex> g(mu_);
            if (id != gatherId_ || i >= gathered_.size()) return; // replaced
            gathered_[i].done = true;
            if (ok) gathered_[i].body = out;
            else gathered_[i].error = err.empty() ? "no answer" : err;
        }).detach();
    }
    return id;
}

std::string Hub::gathered()
{
    std::lock_guard<std::mutex> g(mu_);
    std::string out = "{\"id\":" + std::to_string(gatherId_) + ",\"results\":[";
    for (size_t i = 0; i < gathered_.size(); i++) {
        const auto& r = gathered_[i];
        if (i) out += ",";
        out += "{\"address\":\"" + jsonEscape(r.address) + "\",\"done\":" + (r.done ? "true" : "false") +
               ",\"error\":\"" + jsonEscape(r.error) + "\",\"body\":" + (r.body.empty() ? "null" : r.body) + "}";
    }
    return out + "]}";
}

std::string Hub::jobs()
{
    std::lock_guard<std::mutex> g(mu_);
    std::string out = std::string("{\"recording\":") + (recorder_ > 0 ? "true" : "false") + ",\"jobs\":[";
    for (size_t i = 0; i < jobs_.size(); i++) {
        const Job& j = jobs_[i];
        if (i) out += ",";
        out += "{\"id\":" + std::to_string(j.id) + ",\"kind\":\"" + j.kind + "\",\"state\":\"" + j.state +
               "\",\"name\":\"" + jsonEscape(j.name) + "\",\"path\":\"" + jsonEscape(j.path) +
               "\",\"text\":\"" + jsonEscape(j.text) + "\",\"error\":\"" + jsonEscape(j.error) + "\"}";
    }
    return out + "]}";
}

// --- the outbox ------------------------------------------------------------

namespace {

// A message with the files sent alongside it named at the end, by the path
// on the agent's machine — the phone's withAttachments, in the same words
// but for where they came from.
std::string withAttachments(const std::string& text, const std::vector<std::string>& paths)
{
    if (paths.empty()) return text;
    std::string out = text.empty() ? "" : text + "\n\n";
    out += "Attached from Basecamp (on this machine):";
    for (const auto& p : paths) out += "\n- " + p;
    return out;
}

std::string pctDecode(const std::string& s)
{
    std::string out;
    for (size_t i = 0; i < s.size(); i++) {
        if (s[i] == '%' && i + 2 < s.size() &&
            std::isxdigit(static_cast<unsigned char>(s[i + 1])) && std::isxdigit(static_cast<unsigned char>(s[i + 2]))) {
            out += static_cast<char>(std::stoi(s.substr(i + 1, 2), nullptr, 16));
            i += 2;
        } else {
            out += s[i];
        }
    }
    return out;
}

long long nowMillis()
{
    return std::chrono::duration_cast<std::chrono::milliseconds>(
               std::chrono::system_clock::now().time_since_epoch()).count();
}

std::string newId()
{
    static std::atomic<unsigned> n{0};
    char buf[48];
    std::snprintf(buf, sizeof buf, "b-%llx-%x-%x", static_cast<unsigned long long>(nowMillis()),
                  static_cast<unsigned>(::getpid()), n.fetch_add(1));
    return buf;
}

std::vector<std::string> splitTabs(const std::string& line)
{
    std::vector<std::string> f;
    size_t start = 0;
    for (;;) {
        size_t t = line.find('\t', start);
        f.push_back(line.substr(start, t == std::string::npos ? std::string::npos : t - start));
        if (t == std::string::npos) break;
        start = t + 1;
    }
    return f;
}

}  // namespace

// Where the outbox is kept: the person's data, so ~/.local/share, not a
// cache. Voice notes wait beside the list.
std::string Hub::outboxDir()
{
    return dataDir("outbox");
}

std::string Hub::dataDir(const std::string& sub)
{
    const char* xdg = std::getenv("XDG_DATA_HOME");
    std::string base;
    if (xdg && *xdg) {
        base = xdg;
    } else {
        const char* home = std::getenv("HOME");
        base = std::string(home && *home ? home : "/tmp") + "/.local/share";
    }
    std::string dir = base + "/shrooms/" + sub;
    makeDirs(dir);
    return dir;
}

// One line per entry, tab-separated, text and error percent-encoded so a tab
// or a newline in them cannot break a line.
void Hub::loadOutbox()
{
    if (outboxLoaded_) return;
    outboxLoaded_ = true;
    std::ifstream in(outboxDir() + "/outbox.tsv");
    std::string line;
    while (std::getline(in, line)) {
        auto f = splitTabs(line);
        if (f.size() < 8 || f[0].empty() || (f[3] != "text" && f[3] != "voice")) continue;
        Outgoing o;
        o.id = f[0];
        o.address = f[1];
        o.session = f[2];
        o.kind = f[3];
        o.text = pctDecode(f[4]);
        o.file = f[5];
        o.created = std::atoll(f[6].c_str());
        o.error = pctDecode(f[7]);
        // Files: file|name|sent, each percent-encoded, separated by commas.
        if (f.size() > 8 && !f[8].empty()) {
            size_t start = 0;
            for (;;) {
                size_t comma = f[8].find(',', start);
                std::string one = f[8].substr(start, comma == std::string::npos ? std::string::npos : comma - start);
                size_t p1 = one.find('|'), p2 = p1 == std::string::npos ? p1 : one.find('|', p1 + 1);
                if (p2 != std::string::npos)
                    o.files.push_back({pctDecode(one.substr(0, p1)), pctDecode(one.substr(p1 + 1, p2 - p1 - 1)),
                                       pctDecode(one.substr(p2 + 1))});
                if (comma == std::string::npos) break;
                start = comma + 1;
            }
        }
        outbox_.push_back(o);
    }
}

void Hub::saveOutbox()
{
    std::string path = outboxDir() + "/outbox.tsv";
    std::ofstream out(path + ".tmp", std::ios::trunc);
    for (const auto& o : outbox_) {
        out << o.id << '\t' << o.address << '\t' << o.session << '\t' << o.kind << '\t' << urlEncode(o.text)
            << '\t' << o.file << '\t' << o.created << '\t' << urlEncode(o.error) << '\t';
        for (size_t i = 0; i < o.files.size(); i++) {
            if (i) out << ',';
            out << urlEncode(o.files[i].file) << '|' << urlEncode(o.files[i].name) << '|' << urlEncode(o.files[i].sent);
        }
        out << '\n';
    }
    out.close();
    std::rename((path + ".tmp").c_str(), path.c_str());
}

std::string Hub::keepFile(const std::string& localPath, std::string& err)
{
    std::string data;
    if (!readFile(localPath, data, err)) return "";
    std::string kept = outboxDir() + "/" + newId() + "-" + baseName(localPath);
    std::ofstream out(kept, std::ios::binary);
    out << data;
    out.close();
    if (!out) {
        err = "could not keep " + baseName(localPath);
        ::unlink(kept.c_str());
        return "";
    }
    return kept;
}

std::string Hub::queueText(const std::string& address, const std::string& session, const std::string& text,
                           const std::vector<std::string>& files)
{
    Outgoing o;
    o.id = newId();
    o.address = address;
    o.session = session;
    o.kind = "text";
    o.text = text;
    o.created = nowMillis();
    // Kept files are named <id>-<name>; the agent is told the name only.
    for (const auto& f : files) {
        std::string name = baseName(f);
        if (name.size() > 2 && name[0] == 'b' && name[1] == '-') {
            size_t dash = name.find('-', 2);
            dash = dash == std::string::npos ? dash : name.find('-', dash + 1);
            dash = dash == std::string::npos ? dash : name.find('-', dash + 1);
            if (dash != std::string::npos) name = name.substr(dash + 1);
        }
        o.files.push_back({f, name, ""});
    }
    {
        std::lock_guard<std::mutex> g(mu_);
        loadOutbox();
        outbox_.push_back(o);
        saveOutbox();
    }
    startSender();
    return o.id;
}

std::string Hub::recordSend(const std::string& address, const std::string& session, std::string& err)
{
    int pid;
    std::string path, recorder;
    {
        std::lock_guard<std::mutex> g(mu_);
        pid = recorder_;
        path = recording_;
        recorder = recorderName_;
        recorder_ = -1;
        recording_.clear();
    }
    if (pid <= 0) {
        err = "not recording";
        return "";
    }
    stopRecorder(pid); // the WAV header is written on SIGINT; wait for it
    // Nothing heard is said here, with why, rather than queued and failing
    // at the agent as "the file is empty".
    if (!recordedSomething(path)) {
        err = emptyRecordingWhy(recorder, path);
        ::unlink(path.c_str());
        ::unlink((path + ".err").c_str());
        return "";
    }
    ::unlink((path + ".err").c_str());
    Outgoing o;
    o.id = newId();
    o.address = address;
    o.session = session;
    o.kind = "voice";
    o.file = outboxDir() + "/" + o.id + ".wav";
    o.created = nowMillis();
    if (std::rename(path.c_str(), o.file.c_str()) != 0) {
        std::string body, rerr;
        if (!readFile(path, body, rerr)) {
            err = rerr;
            return "";
        }
        std::ofstream(o.file, std::ios::binary) << body;
        ::unlink(path.c_str());
    }
    {
        std::lock_guard<std::mutex> g(mu_);
        loadOutbox();
        outbox_.push_back(o);
        saveOutbox();
    }
    startSender();
    return o.id;
}

std::string Hub::outbox()
{
    startSender(); // what was left from the last run goes too
    std::lock_guard<std::mutex> g(mu_);
    loadOutbox();
    std::string out = "[";
    for (size_t i = 0; i < outbox_.size(); i++) {
        const auto& o = outbox_[i];
        if (i) out += ",";
        out += "{\"id\":\"" + jsonEscape(o.id) + "\",\"address\":\"" + jsonEscape(o.address) +
               "\",\"session\":\"" + jsonEscape(o.session) + "\",\"kind\":\"" + o.kind +
               "\",\"text\":\"" + jsonEscape(o.text) + "\",\"created\":" + std::to_string(o.created) +
               ",\"error\":\"" + jsonEscape(o.error) + "\",\"files\":[";
        for (size_t j = 0; j < o.files.size(); j++) {
            if (j) out += ",";
            out += "{\"name\":\"" + jsonEscape(o.files[j].name) + "\",\"sent\":" +
                   (o.files[j].sent.empty() ? "false" : "true") + "}";
        }
        out += "]}";
    }
    return out + "]";
}

bool Hub::unqueue(const std::string& id)
{
    std::lock_guard<std::mutex> g(mu_);
    loadOutbox();
    for (auto it = outbox_.begin(); it != outbox_.end(); ++it) {
        if (it->id != id) continue;
        if (!it->file.empty()) ::unlink(it->file.c_str());
        for (const auto& f : it->files) ::unlink(f.file.c_str());
        outbox_.erase(it);
        saveOutbox();
        return true;
    }
    return false;
}

// Files in the outbox's folder that nothing queued names — attached, then
// the message never written — a day after they were kept.
void Hub::sweepOutbox()
{
    static long long last = 0;
    long long now = nowMillis();
    if (now - last < 3600 * 1000LL) return;
    last = now;
    std::set<std::string> named;
    for (const auto& o : outbox_) {
        named.insert(o.file);
        for (const auto& f : o.files) named.insert(f.file);
    }
    std::string dir = outboxDir();
    DIR* d = ::opendir(dir.c_str());
    if (!d) return;
    while (dirent* e = ::readdir(d)) {
        std::string name = e->d_name;
        if (name.compare(0, 2, "b-") != 0) continue; // ours: outbox.tsv and the rest stay
        std::string path = dir + "/" + name;
        struct stat st {};
        if (named.count(path) || ::stat(path.c_str(), &st) != 0) continue;
        if (now / 1000 - st.st_mtime > 24 * 3600) ::unlink(path.c_str());
    }
    ::closedir(d);
}

void Hub::startSender()
{
    std::call_once(senderStarted_, [this]() { sender_ = std::thread([this]() { sendLoop(); }); });
}

// Every few seconds: per session, its oldest entry, and the next only once
// that one has gone — the order things were said in is kept.
void Hub::sendLoop()
{
    while (!stopping_) {
        std::vector<Outgoing> heads;
        {
            std::lock_guard<std::mutex> g(mu_);
            loadOutbox();
            sweepOutbox();
            std::map<std::string, bool> seen;
            for (const auto& o : outbox_) { // in the order queued
                std::string key = o.address + "/" + o.session;
                if (seen[key]) continue;
                seen[key] = true;
                heads.push_back(o);
            }
        }
        bool sent = false;
        for (const auto& o : heads) {
            std::string out, err;
            bool ok;
            if (o.kind == "voice") {
                std::string body;
                ok = readFile(o.file, body, err) &&
                     request(o.address, "POST",
                             "/v1/sessions/" + o.session + "/voice?name=" + urlEncode(baseName(o.file)) + "&id=" + o.id,
                             body, 120, out, err);
            } else {
                // Its files first, each once: where the agent kept it is
                // recorded as soon as it is known, so a failure later does
                // not send it again.
                ok = true;
                std::vector<std::string> paths;
                for (size_t i = 0; ok && i < o.files.size(); i++) {
                    if (!o.files[i].sent.empty()) {
                        paths.push_back(o.files[i].sent);
                        continue;
                    }
                    std::string body;
                    ok = readFile(o.files[i].file, body, err) &&
                         request(o.address, "POST",
                                 "/v1/sessions/" + o.session + "/files?name=" + urlEncode(o.files[i].name), body, 120,
                                 out, err);
                    std::string path = ok ? field(out, "path") : "";
                    if (ok && path.empty()) {
                        ok = false;
                        err = "the agent kept " + o.files[i].name + " nowhere";
                    }
                    if (!ok) break;
                    paths.push_back(path);
                    std::lock_guard<std::mutex> g(mu_);
                    for (auto& q : outbox_)
                        if (q.id == o.id && i < q.files.size()) q.files[i].sent = path;
                    saveOutbox();
                }
                if (ok) {
                    ok = request(o.address, "POST", "/v1/sessions/" + o.session + "/messages",
                                 "{\"text\":\"" + jsonEscape(withAttachments(o.text, paths)) + "\",\"id\":\"" +
                                     jsonEscape(o.id) + "\"}",
                                 15, out, err);
                }
            }
            std::lock_guard<std::mutex> g(mu_);
            for (auto it = outbox_.begin(); it != outbox_.end(); ++it) {
                if (it->id != o.id) continue;
                if (ok) {
                    if (!it->file.empty()) ::unlink(it->file.c_str());
                    for (const auto& f : it->files) ::unlink(f.file.c_str());
                    outbox_.erase(it);
                    sent = true;
                } else {
                    it->error = err;
                }
                break;
            }
            saveOutbox();
        }
        // Straight on while things go; a pause when nothing could, in steps
        // short enough that shutting down does not wait on it.
        for (int i = 0; !sent && !stopping_ && i < (heads.empty() ? 20 : 40); i++)
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
}

}  // namespace agents
