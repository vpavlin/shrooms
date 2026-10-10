#include "shrooms_core_impl.h"
#include "shrooms_agents.h"

#include <cctype>
#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <map>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

#include <dirent.h>
#include <sys/stat.h>

#include <sys/socket.h>
#include <sys/types.h>
#include <sys/un.h>
#include <unistd.h>

namespace {

// Where the daemon listens, and where it listened before the project was
// renamed. Both are tried, for the same reason the daemon itself honours the
// old paths: a node is migrated when its owner gets to it, not when a module
// is installed.
//
// SHROOMS_CONTROL_SOCKET names another, for a daemon started with its own
// --socket and for the tests, which run a stand-in daemon.
const char* defaultSocket()
{
    const char* e = std::getenv("SHROOMS_CONTROL_SOCKET");
    return (e && *e) ? e : "/run/shrooms/shrooms.sock";
}
const char* kSocket = defaultSocket();
const char* kLegacySocket = "/run/logos-vpn/logos-vpn.sock";

// A status document is a few kilobytes. This bound is generous and exists so a
// misbehaving peer cannot make us read without limit.
constexpr size_t kMaxResponse = 1 << 20;

// How much of a refused request's body to quote back. Enough for the daemon's
// own sentence about what was wrong, short enough that a stray HTML error page
// does not become the error message.
constexpr size_t kMaxErrorDetail = 200;

std::string jsonEscape(const std::string& s)
{
    std::string out;
    out.reserve(s.size() + 8);
    for (char c : s) {
        switch (c) {
        case '"':  out += "\\\""; break;
        case '\\': out += "\\\\"; break;
        case '\n': out += "\\n";  break;
        case '\r': out += "\\r";  break;
        case '\t': out += "\\t";  break;
        case '\b': out += "\\b";  break;
        case '\f': out += "\\f";  break;
        default:
            if (static_cast<unsigned char>(c) < 0x20) {
                // Every remaining control character has to go out as \uXXXX;
                // JSON forbids them raw inside a string. The value is widened
                // through unsigned char because char is signed on the targets
                // this builds for, and passing it straight to a %x conversion
                // is undefined behaviour the moment the byte has its top bit
                // set.
                char buf[7];
                std::snprintf(buf, sizeof(buf), "\\u%04x",
                              static_cast<unsigned>(static_cast<unsigned char>(c)));
                out += buf;
            } else {
                // Bytes at 0x80 and above are passed through untouched, which
                // is what keeps a name like "Küche" arriving as itself: the
                // input is already UTF-8 and JSON strings are UTF-8, so
                // escaping them would only mangle what is correct.
                out += c;
            }
        }
    }
    return out;
}

/**
 * One JSON string literal, quotes included.
 *
 * Every body below is assembled by concatenation, and a bare jsonEscape() call
 * in the middle of one looks exactly like a correctly quoted value while
 * producing an unquoted one. Making the quotes part of the helper removes the
 * chance to get that wrong.
 */
std::string jsonString(const std::string& s)
{
    return "\"" + jsonEscape(s) + "\"";
}

std::string errorJson(const std::string& what, const std::string& detail)
{
    return "{\"error\":\"" + jsonEscape(what) + "\",\"detail\":\"" +
           jsonEscape(detail) + "\"}";
}

/**
 * Why a request did not produce a body.
 *
 * A bool would do for reading, and did. Writing needs the distinction: the
 * daemon may live on either of two socket paths, and falling back from one to
 * the other is only safe when the first was never reached. If the daemon
 * answered at all — even with a 500 — it may have already applied the change,
 * and retrying the same POST elsewhere would be a second write, not a retry.
 */
enum class RequestOutcome {
    Ok,
    // Nothing is listening there, so nothing was done and nothing was read.
    Unreachable,
    // We spoke to something and it did not give us a body we can return.
    Failed,
};

/**
 * One HTTP request over a unix socket, returning the response body.
 *
 * Hand-rolled rather than pulled in: the daemon speaks HTTP/1.1 over
 * AF_UNIX and this needs exactly one request with no keep-alive, no
 * redirects and no TLS. A dependency for that would be larger than the
 * problem.
 *
 * GET and POST differ only in the request line and in whether a body follows
 * the headers, so they share this. They were briefly two functions and the
 * copies had already begun to drift — the response size bound was tightened in
 * one of them and not the other, which is the sort of divergence that is
 * invisible until the day it matters.
 *
 * An empty `contentType` suppresses the header entirely, for the endpoints that
 * take no body at all.
 */
RequestOutcome httpRequestUnix(const std::string& path, const std::string& method,
                               const std::string& target, const std::string& contentType,
                               const std::string& requestBody, std::string& body,
                               std::string& err, int timeoutS = 2)
{
    int fd = ::socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) {
        err = std::string("socket: ") + std::strerror(errno);
        return RequestOutcome::Unreachable;
    }

    sockaddr_un addr{};
    addr.sun_family = AF_UNIX;
    if (path.size() >= sizeof(addr.sun_path)) {
        ::close(fd);
        err = "socket path is too long";
        return RequestOutcome::Unreachable;
    }
    std::memcpy(addr.sun_path, path.c_str(), path.size());

    // Bounded, so a wedged daemon cannot hang the caller. The view calls this
    // synchronously from the UI thread; a blocking read there would freeze
    // Basecamp, which is a far worse outcome than showing stale numbers.
    //
    // The one exception is a join, which waits for the far side by design and
    // runs on a thread of its own (joinWithInviteStart), never on the view's.
    timeval tv{};
    tv.tv_sec = timeoutS;
    ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    ::setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));

    if (::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        err = std::string("connect ") + path + ": " + std::strerror(errno);
        ::close(fd);
        return RequestOutcome::Unreachable;
    }

    std::string req = method + " " + target +
                      " HTTP/1.0\r\nHost: unix\r\nConnection: close\r\n";
    if (!contentType.empty()) {
        req += "Content-Type: " + contentType + "\r\n";
    }
    // Sent even when the body is empty. Without it a POST is a request whose
    // body length the daemon has to guess at, and Go's http server treats a
    // POST with neither Content-Length nor chunked encoding as having no body
    // — which turns a config change into a silent no-op rather than an error.
    if (method != "GET") {
        req += "Content-Length: " + std::to_string(requestBody.size()) + "\r\n";
    }
    req += "\r\n";
    req += requestBody;

    size_t sent = 0;
    while (sent < req.size()) {
        ssize_t n = ::write(fd, req.data() + sent, req.size() - sent);
        if (n <= 0) {
            err = std::string("write: ") + std::strerror(errno);
            ::close(fd);
            // The connect succeeded, so something is there and may have read
            // part of this. Not safe to send again anywhere else.
            return RequestOutcome::Failed;
        }
        sent += static_cast<size_t>(n);
    }

    std::string raw;
    char buf[4096];
    for (;;) {
        ssize_t n = ::read(fd, buf, sizeof(buf));
        if (n < 0) {
            err = std::string("read: ") + std::strerror(errno);
            ::close(fd);
            return RequestOutcome::Failed;
        }
        if (n == 0) break;
        raw.append(buf, static_cast<size_t>(n));
        if (raw.size() > kMaxResponse) {
            err = "response is implausibly large";
            ::close(fd);
            return RequestOutcome::Failed;
        }
    }
    ::close(fd);

    const auto sep = raw.find("\r\n\r\n");
    if (sep == std::string::npos) {
        err = "no HTTP header terminator in the reply";
        return RequestOutcome::Failed;
    }
    // The status line, checked rather than assumed: a 404 body is not status,
    // and returning it would put a decoding error in front of the user instead
    // of the real one.
    if (raw.compare(0, 9, "HTTP/1.1 ") != 0 && raw.compare(0, 9, "HTTP/1.0 ") != 0) {
        err = "the reply is not HTTP";
        return RequestOutcome::Failed;
    }
    const std::string code = raw.substr(9, 3);
    // Any 2xx, not 200 alone. The write endpoints have no body to return and
    // answer 204; insisting on 200 would report every successful reload as a
    // failure and invite the user to run it again.
    if (code.size() != 3 || code[0] != '2') {
        err = "the daemon answered " + code;
        // The daemon's own account of the refusal is the useful part — "invite
        // expired" says what to do next, where the bare number does not — so it
        // is carried out to the view rather than dropped here.
        std::string detail = raw.substr(sep + 4);
        if (detail.size() > kMaxErrorDetail) {
            detail.resize(kMaxErrorDetail);
        }
        if (!detail.empty()) {
            err += ": " + detail;
        }
        return RequestOutcome::Failed;
    }

    body = raw.substr(sep + 4);
    return RequestOutcome::Ok;
}

RequestOutcome httpGetUnix(const std::string& path, const std::string& target,
                           std::string& body, std::string& err)
{
    return httpRequestUnix(path, "GET", target, "", "", body, err);
}

RequestOutcome httpPostUnix(const std::string& path, const std::string& target,
                            const std::string& contentType, const std::string& requestBody,
                            std::string& body, std::string& err)
{
    return httpRequestUnix(path, "POST", target, contentType, requestBody, body, err);
}

/**
 * Adds the one piece of advice that a transport error cannot carry itself.
 *
 * Permission is the failure worth naming, because it has a one-line fix and
 * looks identical to "the daemon is not running" from here. The daemon needs
 * CAP_NET_ADMIN and so runs as root; its socket is 0660, and Basecamp is not
 * root.
 */
std::string withPermissionHint(const std::string& err)
{
    if (err.find("Permission denied") != std::string::npos) {
        return err + " — set socket_group in the daemon's config to a group you are in";
    }
    return err;
}

/**
 * The one JSON document a write turns into, whatever happened.
 *
 * Every method here promises the view a JSON object, and a bare success from
 * the daemon does not supply one: the config endpoints have nothing to say and
 * answer 204 with no body at all. Handing that empty string back would make a
 * change that worked indistinguishable from one that vanished, which is the
 * same "nothing appeared" that status() returns an error rather than produce.
 */
std::string writeResult(RequestOutcome outcome, const std::string& body,
                        const std::string& err)
{
    if (outcome == RequestOutcome::Ok) {
        return body.empty() ? std::string("{\"ok\":true}") : body;
    }

    // A daemon that answered and said no is a different problem for the user
    // than a daemon that is not there — one is a bad token or a name it will
    // not take, the other is a service to start — and the two were worth
    // telling apart in the sentence the view puts on screen.
    const char* what = outcome == RequestOutcome::Unreachable
                           ? "cannot reach the Shrooms daemon"
                           : "the Shrooms daemon refused the request";
    return errorJson(what, withPermissionHint(err));
}

/**
 * A JSON POST to whichever of the two socket paths the daemon is on.
 *
 * The legacy path is only tried when the first was not reached at all, which is
 * the distinction RequestOutcome exists to draw. A daemon that answered has
 * possibly already acted, and a mesh joined twice because this function was
 * helpful is not a better outcome than an error message.
 */
std::string postToDaemon(const std::string& target, const std::string& requestBody)
{
    std::string body, err, firstErr;
    RequestOutcome outcome = RequestOutcome::Unreachable;

    for (const char* path : {kSocket, kLegacySocket}) {
        outcome = httpPostUnix(path, target, requestBody.empty() ? "" : "application/json",
                               requestBody, body, err);
        if (outcome != RequestOutcome::Unreachable) {
            return writeResult(outcome, body, err);
        }
        // The first path's error is the one reported. It names the socket the
        // node is supposed to be using, where the legacy path's error would
        // send whoever reads it looking in a directory that was renamed.
        if (firstErr.empty()) firstErr = err;
    }

    return writeResult(outcome, body, firstErr);
}

/** As postToDaemon(), but on the one socket the caller named. */
std::string postToSocket(const std::string& socketPath, const std::string& target,
                         const std::string& requestBody)
{
    std::string body, err;
    const RequestOutcome outcome =
        httpPostUnix(socketPath, target, requestBody.empty() ? "" : "application/json",
                     requestBody, body, err);
    return writeResult(outcome, body, err);
}

/**
 * Turns `immich:2283, jellyfin:8096` into `["immich:2283","jellyfin:8096"]`.
 *
 * Surrounding whitespace is dropped from each entry because a person typing a
 * list into a text field puts a space after the comma, and a spec of
 * " jellyfin:8096" is rejected by the daemon for a reason no one reading the
 * field back would ever guess. Empty entries go too, so a trailing comma is not
 * an error either.
 *
 * Nothing else is checked. Whether a spec is well formed is the daemon's
 * judgement, and duplicating it here would only produce a second, subtly
 * different answer.
 */
std::string servicesArray(const std::string& csv)
{
    std::string out = "[";
    bool first = true;

    size_t pos = 0;
    while (pos <= csv.size()) {
        size_t comma = csv.find(',', pos);
        if (comma == std::string::npos) comma = csv.size();

        size_t begin = pos;
        size_t end = comma;
        while (begin < end && std::isspace(static_cast<unsigned char>(csv[begin]))) ++begin;
        while (end > begin && std::isspace(static_cast<unsigned char>(csv[end - 1]))) --end;

        if (end > begin) {
            if (!first) out += ",";
            out += jsonString(csv.substr(begin, end - begin));
            first = false;
        }

        pos = comma + 1;
    }

    out += "]";
    return out;
}

/**
 * The /logs path, with a `since` when there is one worth sending.
 *
 * Built here rather than at the call sites so the two of them cannot disagree
 * about whether zero means "everything" or "everything since the epoch" —
 * which are the same set today and would stop being on the day the daemon
 * learns to keep a longer tail.
 */
std::string logsPath(const std::string& sinceMs)
{
    // Digits only, and a bounded number of them. This value lands in a URL
    // query, so anything else in it would be an injection into the request
    // line; the daemon ignores a `since` it cannot parse, which makes
    // dropping a malformed one the same as asking for everything.
    if (sinceMs.empty() || sinceMs.size() > 19) return "/logs";
    for (char c : sinceMs) {
        if (!std::isdigit(static_cast<unsigned char>(c))) return "/logs";
    }
    if (sinceMs.find_first_not_of('0') == std::string::npos) return "/logs";
    return "/logs?since=" + sinceMs;
}

/**
 * Where view preferences live.
 *
 * Under XDG config rather than beside the daemon's own state: these belong to
 * the person looking at the window, not to the node, and a desktop preference
 * in /etc is a file nobody will ever find again.
 */
std::string prefPath()
{
    const char* xdg = std::getenv("XDG_CONFIG_HOME");
    std::string base;
    if (xdg && *xdg) {
        base = xdg;
    } else {
        const char* home = std::getenv("HOME");
        if (!home || !*home) return "";
        base = std::string(home) + "/.config";
    }
    base += "/shrooms";
    // Best effort, and the failure is handled by the write failing after it:
    // a preference that cannot be saved is not worth an error path of its own.
    agents::makeDirs(base);
    return base + "/view.conf";
}

/** Keys are written by this view, so anything unexpected is a bug, not input. */
bool validKey(const std::string& k)
{
    if (k.empty() || k.size() > 64) return false;
    for (char c : k) {
        if (!std::isalnum(static_cast<unsigned char>(c)) &&
            c != '_' && c != '.' && c != '-') {
            return false;
        }
    }
    return true;
}

/** Every stored preference, as a map. Missing file is an empty map. */
std::map<std::string, std::string> readPrefs()
{
    std::map<std::string, std::string> out;
    const std::string path = prefPath();
    if (path.empty()) return out;
    std::ifstream f(path);
    std::string line;
    while (std::getline(f, line)) {
        const auto eq = line.find('=');
        if (eq == std::string::npos) continue;
        const std::string k = line.substr(0, eq);
        if (!validKey(k)) continue;
        out[k] = line.substr(eq + 1);
    }
    return out;
}

} // namespace

std::string ShroomsCoreImpl::statusFrom(const std::string& socketPath)
{
    std::string body, err;
    if (httpGetUnix(socketPath, "/status", body, err) == RequestOutcome::Ok) {
        return body;
    }
    return errorJson("cannot read the Shrooms daemon", err);
}

std::string ShroomsCoreImpl::status()
{
    std::string body, err, firstErr;

    // Unlike the writes, this retries whatever went wrong on the first path.
    // Reading twice costs nothing, and a daemon that answered badly on one
    // socket is no reason not to ask the other.
    for (const char* path : {kSocket, kLegacySocket}) {
        if (httpGetUnix(path, "/status", body, err) == RequestOutcome::Ok) {
            return body;
        }
        if (firstErr.empty()) firstErr = err;
    }

    return errorJson("cannot read the Shrooms daemon", withPermissionHint(firstErr));
}

std::string ShroomsCoreImpl::hostsSuffixFrom(const std::string& socketPath)
{
    std::string body, err;
    if (httpGetUnix(socketPath, "/config/hosts-suffix", body, err) == RequestOutcome::Ok) {
        return body;
    }
    return errorJson("cannot read the domain suffix", err);
}

std::string ShroomsCoreImpl::hostsSuffix()
{
    std::string body, err, firstErr;
    for (const char* path : {kSocket, kLegacySocket}) {
        if (httpGetUnix(path, "/config/hosts-suffix", body, err) == RequestOutcome::Ok) {
            return body;
        }
        if (firstErr.empty()) firstErr = err;
    }
    return errorJson("cannot read the domain suffix", withPermissionHint(firstErr));
}

std::string ShroomsCoreImpl::setHostsSuffixOn(const std::string& socketPath,
                                              const std::string& suffix)
{
    return postToSocket(socketPath, "/config/hosts-suffix",
                        "{\"hosts_suffix\":" + jsonString(suffix) + "}");
}

std::string ShroomsCoreImpl::setHostsSuffix(const std::string& suffix)
{
    return postToDaemon("/config/hosts-suffix",
                        "{\"hosts_suffix\":" + jsonString(suffix) + "}");
}

std::string ShroomsCoreImpl::servicesFrom(const std::string& socketPath)
{
    std::string body, err;
    if (httpGetUnix(socketPath, "/config/services", body, err) == RequestOutcome::Ok) {
        return body;
    }
    return errorJson("cannot read the configured services", err);
}

std::string ShroomsCoreImpl::services()
{
    std::string body, err, firstErr;
    for (const char* path : {kSocket, kLegacySocket}) {
        if (httpGetUnix(path, "/config/services", body, err) == RequestOutcome::Ok) {
            return body;
        }
        if (firstErr.empty()) firstErr = err;
    }
    return errorJson("cannot read the configured services", withPermissionHint(firstErr));
}

std::string ShroomsCoreImpl::setNameOn(const std::string& socketPath, const std::string& name)
{
    return postToSocket(socketPath, "/config/name", "{\"name\":" + jsonString(name) + "}");
}

std::string ShroomsCoreImpl::setName(const std::string& name)
{
    return postToDaemon("/config/name", "{\"name\":" + jsonString(name) + "}");
}

std::string ShroomsCoreImpl::setModeOn(const std::string& socketPath, const std::string& mode)
{
    return postToSocket(socketPath, "/config/mode", "{\"mode\":" + jsonString(mode) + "}");
}

std::string ShroomsCoreImpl::setMode(const std::string& mode)
{
    return postToDaemon("/config/mode", "{\"mode\":" + jsonString(mode) + "}");
}

std::string ShroomsCoreImpl::setServicesOn(const std::string& socketPath, const std::string& csv)
{
    return postToSocket(socketPath, "/config/services",
                        "{\"services\":" + servicesArray(csv) + "}");
}

std::string ShroomsCoreImpl::setServices(const std::string& csv)
{
    return postToDaemon("/config/services", "{\"services\":" + servicesArray(csv) + "}");
}

namespace {
/** The body every per-mesh flag sends: which mesh, and on or off. */
std::string flagBody(const std::string& label, bool on)
{
    return "{\"label\":" + jsonString(label) +
           ",\"enabled\":" + (on ? "true" : "false") + "}";
}
} // namespace

std::string ShroomsCoreImpl::setRelay(const std::string& label, bool on)
{
    return postToDaemon("/config/relay", flagBody(label, on));
}

std::string ShroomsCoreImpl::setRelayOn(const std::string& socketPath,
                                        const std::string& label, bool on)
{
    return postToSocket(socketPath, "/config/relay", flagBody(label, on));
}

std::string ShroomsCoreImpl::setPortMapping(bool on)
{
    return postToDaemon("/config/portmap",
                        std::string("{\"enabled\":") + (on ? "true" : "false") + "}");
}

std::string ShroomsCoreImpl::setPortMappingOn(const std::string& socketPath, bool on)
{
    return postToSocket(socketPath, "/config/portmap",
                        std::string("{\"enabled\":") + (on ? "true" : "false") + "}");
}

std::string ShroomsCoreImpl::setAnnounceBound(const std::string& label, bool on)
{
    return postToDaemon("/config/announce-bound", flagBody(label, on));
}

std::string ShroomsCoreImpl::setAnnounceBoundOn(const std::string& socketPath,
                                                const std::string& label, bool on)
{
    return postToSocket(socketPath, "/config/announce-bound", flagBody(label, on));
}

std::string ShroomsCoreImpl::setMeshEnabledOn(const std::string& socketPath,
                                              const std::string& label, bool enabled)
{
    return postToSocket(socketPath, "/config/mesh",
                        "{\"label\":" + jsonString(label) +
                            ",\"enabled\":" + (enabled ? "true" : "false") + "}");
}

std::string ShroomsCoreImpl::setAnnounceServices(const std::string& label, bool on)
{
    return postToDaemon("/config/announce", flagBody(label, on));
}

std::string ShroomsCoreImpl::setAnnounceServicesOn(const std::string& socketPath,
                                                   const std::string& label, bool on)
{
    return postToSocket(socketPath, "/config/announce", flagBody(label, on));
}

std::string ShroomsCoreImpl::setMeshEnabled(const std::string& label, bool enabled)
{
    return postToDaemon("/config/mesh",
                        "{\"label\":" + jsonString(label) +
                            ",\"enabled\":" + (enabled ? "true" : "false") + "}");
}

std::string ShroomsCoreImpl::joinWithInviteOn(const std::string& socketPath,
                                              const std::string& token, const std::string& name,
                                              const std::string& label)
{
    return postToSocket(socketPath, "/join",
                        "{\"token\":" + jsonString(token) + ",\"name\":" + jsonString(name) +
                            ",\"label\":" + jsonString(label) + "}");
}

std::string ShroomsCoreImpl::joinWithInvite(const std::string& token, const std::string& name,
                                            const std::string& label)
{
    return postToDaemon("/join",
                        "{\"token\":" + jsonString(token) + ",\"name\":" + jsonString(name) +
                            ",\"label\":" + jsonString(label) + "}");
}

std::string ShroomsCoreImpl::leaveMeshOn(const std::string& socketPath, const std::string& label)
{
    return postToSocket(socketPath, "/leave", "{\"label\":" + jsonString(label) + "}");
}

std::string ShroomsCoreImpl::leaveMesh(const std::string& label)
{
    return postToDaemon("/leave", "{\"label\":" + jsonString(label) + "}");
}

// The log tail is a read, so it retries the second socket path the way status()
// does rather than stopping at the first failure: reading twice costs nothing.
std::string ShroomsCoreImpl::getPref(const std::string& key)
{
    if (!validKey(key)) return "";
    const auto prefs = readPrefs();
    const auto it = prefs.find(key);
    return it == prefs.end() ? std::string() : it->second;
}

std::string ShroomsCoreImpl::setPref(const std::string& key, const std::string& value)
{
    if (!validKey(key)) {
        return errorJson("that is not a preference key", key);
    }
    // A newline in a value would write a line this cannot read back, so it is
    // refused rather than mangled — no preference here is meant to contain one.
    if (value.find('\n') != std::string::npos || value.find('\r') != std::string::npos) {
        return errorJson("a preference cannot contain a line break", key);
    }

    auto prefs = readPrefs();
    if (value.empty()) {
        prefs.erase(key);
    } else {
        prefs[key] = value;
    }

    const std::string path = prefPath();
    if (path.empty()) {
        return errorJson("no writable config directory", "neither XDG_CONFIG_HOME nor HOME is set");
    }
    // Written whole and renamed into place: a half-written file would be read
    // back as a set of preferences somebody never chose.
    const std::string tmp = path + ".tmp";
    {
        std::ofstream f(tmp, std::ios::trunc);
        if (!f) {
            return errorJson("cannot write preferences", tmp);
        }
        for (const auto& kv : prefs) {
            f << kv.first << "=" << kv.second << "\n";
        }
        if (!f) {
            return errorJson("cannot write preferences", tmp);
        }
    }
    if (std::rename(tmp.c_str(), path.c_str()) != 0) {
        return errorJson("cannot save preferences", std::strerror(errno));
    }
    return "{\"result\":\"saved\"}";
}

std::string ShroomsCoreImpl::logsFrom(const std::string& socketPath, const std::string& sinceMs)
{
    std::string body, err;
    if (httpGetUnix(socketPath, logsPath(sinceMs), body, err) == RequestOutcome::Ok) {
        return body;
    }
    return errorJson("cannot read the Shrooms daemon", err);
}

std::string ShroomsCoreImpl::logs(const std::string& sinceMs)
{
    std::string body, err, firstErr;
    const std::string path = logsPath(sinceMs);
    for (const char* sock : {kSocket, kLegacySocket}) {
        if (httpGetUnix(sock, path, body, err) == RequestOutcome::Ok) {
            return body;
        }
        if (firstErr.empty()) firstErr = err;
    }
    return errorJson("cannot read the Shrooms daemon", withPermissionHint(firstErr));
}

// Unlike the reads, this does not fall back to the second socket path. The
// daemon answers and then exits, so a response that never arrives does not mean
// nothing happened — and asking the other path would be a second restart of a
// daemon that is already on its way down.
std::string ShroomsCoreImpl::restartOn(const std::string& socketPath)
{
    return postToSocket(socketPath, "/restart", "");
}

std::string ShroomsCoreImpl::restart()
{
    return postToDaemon("/restart", "");
}

std::string ShroomsCoreImpl::reloadOn(const std::string& socketPath)
{
    return postToSocket(socketPath, "/reload", "");
}

std::string ShroomsCoreImpl::reload()
{
    return postToDaemon("/reload", "");
}

// --- settings by mesh, relays, revocations ---------------------------------

namespace {

/**
 * A mesh label fit to go into a query string, or "" when it is not one.
 *
 * Labels are the daemon's own (letters, digits, dot, dash, underscore), so
 * anything else is refused rather than escaped: a label that needs escaping is
 * not one the daemon could have given the view.
 */
std::string labelQuery(const std::string& label)
{
    if (label.empty()) return "";
    if (label.size() > 64) return "!";
    for (unsigned char c : label) {
        if (!std::isalnum(c) && c != '.' && c != '-' && c != '_') return "!";
    }
    return "?mesh=" + label;
}

/** A GET to whichever socket path answers, as status() reads. */
std::string getFromDaemon(const std::string& target, const std::string& what)
{
    std::string body, err, firstErr;
    for (const char* path : {kSocket, kLegacySocket}) {
        if (httpGetUnix(path, target, body, err) == RequestOutcome::Ok) {
            return body;
        }
        if (firstErr.empty()) firstErr = err;
    }
    return errorJson(what, withPermissionHint(firstErr));
}

} // namespace

std::string ShroomsCoreImpl::servicesOf(const std::string& label)
{
    const std::string q = labelQuery(label);
    if (q == "!") return errorJson("that is not a mesh label", label);
    return getFromDaemon("/config/services" + q, "cannot read the configured services");
}

std::string ShroomsCoreImpl::setServicesOf(const std::string& label, const std::string& csv)
{
    return postToDaemon("/config/services", "{\"label\":" + jsonString(label) +
                                                ",\"services\":" + servicesArray(csv) + "}");
}

std::string ShroomsCoreImpl::setAnnounceRevocations(const std::string& label, bool on)
{
    return postToDaemon("/config/announce-revocations", flagBody(label, on));
}

std::string ShroomsCoreImpl::blindRelays()
{
    return getFromDaemon("/config/blind-relays", "cannot read the blind relays");
}

std::string ShroomsCoreImpl::setBlindRelays(const std::string& label, const std::string& relays)
{
    return postToDaemon("/config/blind-relays",
                        "{\"label\":" + jsonString(label) + ",\"relays\":" + jsonString(relays) + "}");
}

std::string ShroomsCoreImpl::setBlindRelaysWithToken(const std::string& label, const std::string& relays,
                                                     const std::string& token)
{
    return postToDaemon("/config/blind-relays",
                        "{\"label\":" + jsonString(label) + ",\"relays\":" + jsonString(relays) +
                            ",\"token\":" + jsonString(token) + "}");
}

// --- long calls, off the view's thread -----------------------------------------
//
// A join waits for the far side — up to two minutes by the daemon's default —
// and so does holding an invite open, for up to fifteen. Every other call here
// gives up after two seconds, because the view calls them on its own thread and
// a blocked call is a frozen Basecamp. So a join called the ordinary way
// reported a timeout every time, including the times it went on to succeed.
// These run on a thread of their own, with a deadline that fits, and the view
// asks how they are going.

namespace {

struct LongCall {
    bool running = false;
    std::string result;   // the last call's answer, JSON
    unsigned serial = 0;  // which call that answer belongs to
};

std::mutex& longMu()
{
    static std::mutex mu;
    return mu;
}

std::map<std::string, LongCall>& longCalls()
{
    static std::map<std::string, LongCall> calls;
    return calls;
}

// The daemon's own wait is two minutes; this is that and room to answer.
constexpr int kJoinTimeoutS = 150;
// An invite stays open fifteen minutes (invite.DefaultTTL).
constexpr int kHoldTimeoutS = 16 * 60;

/**
 * Starts a POST on a thread and returns at once. A call already running under
 * the same name is refused when `exclusive`, and otherwise superseded: its
 * answer, when it comes, is dropped.
 */
std::string startLong(const std::string& name, const std::string& target, const std::string& body,
                      int timeoutS, bool exclusive)
{
    unsigned serial;
    {
        std::lock_guard<std::mutex> lock(longMu());
        LongCall& c = longCalls()[name];
        if (c.running && exclusive) {
            return errorJson("a " + name + " is already running", "wait for it to finish");
        }
        c.running = true;
        c.result.clear();
        serial = ++c.serial;
    }
    std::thread([name, target, body, timeoutS, serial]() {
        std::string out, err, firstErr;
        RequestOutcome outcome = RequestOutcome::Unreachable;
        // Not retried on the legacy path once anything answered: a daemon that
        // heard the request may have acted, and joining twice spends the
        // invite.
        for (const char* path : {kSocket, kLegacySocket}) {
            outcome = httpRequestUnix(path, "POST", target, "application/json", body, out, err, timeoutS);
            if (outcome != RequestOutcome::Unreachable) break;
            if (firstErr.empty()) firstErr = err;
        }
        std::string result = writeResult(outcome, out,
                                         outcome == RequestOutcome::Unreachable ? firstErr : err);
        std::lock_guard<std::mutex> lock(longMu());
        LongCall& c = longCalls()[name];
        if (c.serial == serial) {
            c.running = false;
            c.result = result;
        }
    }).detach();
    return "{\"started\":true,\"serial\":" + std::to_string(serial) + "}";
}

std::string longProgress(const std::string& name)
{
    std::lock_guard<std::mutex> lock(longMu());
    auto it = longCalls().find(name);
    if (it == longCalls().end() || (!it->second.running && it->second.result.empty())) {
        return "{\"idle\":true}";
    }
    const LongCall& c = it->second;
    if (c.running) {
        return "{\"running\":true,\"serial\":" + std::to_string(c.serial) + "}";
    }
    return "{\"done\":true,\"serial\":" + std::to_string(c.serial) + ",\"result\":" + c.result + "}";
}

void forgetLong(const std::string& name)
{
    std::lock_guard<std::mutex> lock(longMu());
    LongCall& c = longCalls()[name];
    c.running = false;
    c.result.clear();
    ++c.serial;  // whatever is still in flight answers nobody
}

} // namespace

std::string ShroomsCoreImpl::joinWithInviteStart(const std::string& token, const std::string& name,
                                                 const std::string& label)
{
    return startLong("join", "/join",
                     "{\"token\":" + jsonString(token) + ",\"name\":" + jsonString(name) +
                         ",\"label\":" + jsonString(label) + "}",
                     kJoinTimeoutS, true);
}

std::string ShroomsCoreImpl::joinProgress()
{
    return longProgress("join");
}

// --- inviting, with a signature made elsewhere (ADR-050) ----------------------
//
// The daemon does every part that knows a format — the token, the credential,
// checking the signature — and the card is another module's, which the view
// asks itself. What is here is carrying requests, and finding which account on
// the card is this mesh's.

std::string ShroomsCoreImpl::inviteNew(const std::string& mesh)
{
    return postToDaemon("/invite/new", "{\"mesh\":" + jsonString(mesh) + "}");
}

std::string ShroomsCoreImpl::inviteHoldStart(const std::string& token, const std::string& mesh)
{
    // Superseding rather than refusing: a person who closes one invite and
    // opens another means the second.
    return startLong("hold", "/invite/hold",
                     "{\"token\":" + jsonString(token) + ",\"mesh\":" + jsonString(mesh) + "}",
                     kHoldTimeoutS, false);
}

std::string ShroomsCoreImpl::inviteHoldProgress()
{
    return longProgress("hold");
}

std::string ShroomsCoreImpl::inviteHoldCancel()
{
    forgetLong("hold");
    return "{\"result\":\"stopped waiting\"}";
}

std::string ShroomsCoreImpl::inviteDraft(const std::string& mesh, const std::string& devicePub,
                                         const std::string& wgPub, const std::string& sealPub,
                                         const std::string& name)
{
    return postToDaemon("/invite/draft",
                        "{\"mesh\":" + jsonString(mesh) + ",\"device_pub\":" + jsonString(devicePub) +
                            ",\"wg_pub\":" + jsonString(wgPub) + ",\"seal_pub\":" + jsonString(sealPub) +
                            ",\"name\":" + jsonString(name) + "}");
}

std::string ShroomsCoreImpl::inviteReply(const std::string& token, const std::string& ephPub,
                                         const std::string& name, const std::string& mesh,
                                         const std::string& draft, const std::string& signature)
{
    std::string body = "{\"token\":" + jsonString(token) + ",\"eph_pub\":" + jsonString(ephPub) +
                       ",\"name\":" + jsonString(name) + ",\"mesh\":" + jsonString(mesh);
    if (!draft.empty()) body += ",\"credential\":" + jsonString(draft);
    if (!signature.empty()) body += ",\"signature\":" + jsonString(signature);
    return postToDaemon("/invite/reply", body + "}");
}

namespace {

/** The digits after "account": in an admin file, or -1 when it says none. */
long accountIn(const std::string& doc)
{
    const auto at = doc.find("\"account\"");
    if (at == std::string::npos) return -1;
    size_t i = doc.find(':', at);
    if (i == std::string::npos) return -1;
    ++i;
    while (i < doc.size() && std::isspace(static_cast<unsigned char>(doc[i]))) ++i;
    long n = 0;
    bool any = false;
    while (i < doc.size() && std::isdigit(static_cast<unsigned char>(doc[i])) && n < 1000000) {
        n = n * 10 + (doc[i] - '0');
        ++i;
        any = true;
    }
    return any ? n : -1;
}

/** Where `shrooms admin` keeps its files: the same directory as view.conf. */
std::string adminDir()
{
    const std::string pref = prefPath();
    const auto slash = pref.rfind('/');
    return slash == std::string::npos ? std::string() : pref.substr(0, slash);
}

} // namespace

std::string ShroomsCoreImpl::cardPath(const std::string& adminKeysCsv)
{
    // Which key on the card signs for this mesh is not on the card: it is the
    // account `shrooms admin init --keycard` chose, recorded in the admin file
    // beside the public keys (ADR-022). Found here by key rather than by label,
    // because labels are local and the file may have been named for another.
    std::vector<std::string> keys;
    size_t pos = 0;
    while (pos <= adminKeysCsv.size()) {
        size_t comma = adminKeysCsv.find(',', pos);
        if (comma == std::string::npos) comma = adminKeysCsv.size();
        std::string k = adminKeysCsv.substr(pos, comma - pos);
        bool ok = !k.empty() && k.size() <= 128;
        for (unsigned char c : k) {
            if (!std::isalnum(c)) ok = false;
        }
        if (ok) keys.push_back(k);
        pos = comma + 1;
    }
    const std::string dir = adminDir();
    long account = -1;
    std::string from;
    if (!dir.empty() && !keys.empty()) {
        if (DIR* d = ::opendir(dir.c_str())) {
            while (dirent* e = ::readdir(d)) {
                const std::string n = e->d_name;
                if (n.rfind("admin", 0) != 0 || n.size() < 10 || n.substr(n.size() - 5) != ".json") continue;
                std::ifstream f(dir + "/" + n);
                std::stringstream ss;
                ss << f.rdbuf();
                const std::string doc = ss.str();
                bool mine = false;
                for (const auto& k : keys) {
                    if (doc.find("\"" + k + "\"") != std::string::npos) mine = true;
                }
                if (!mine) continue;
                account = accountIn(doc);
                if (account < 0) account = 0;  // absent means 0 (ADR-022)
                from = n;
                break;
            }
            ::closedir(d);
        }
    }
    const bool known = account >= 0;
    if (!known) account = 0;
    return "{\"bip32_path\":\"m/64265'/" + std::to_string(account) + "'/0'\",\"account\":" +
           std::to_string(account) + ",\"known\":" + (known ? "true" : "false") +
           (from.empty() ? std::string() : ",\"from\":" + jsonString(from)) + "}";
}

// --- agents ------------------------------------------------------------------
//
// The hub lives here rather than as a member: the module glue reads the class
// declaration, and it has no reason to see the threads behind these methods.

namespace {

agents::Hub& hub()
{
    static agents::Hub h;
    return h;
}

bool safeSession(const std::string& s)
{
    if (s.empty() || s.size() > 64) return false;
    for (unsigned char c : s) {
        if (!std::isalnum(c) && c != '.' && c != '_' && c != '-') return false;
    }
    return true;
}

}  // namespace

std::string ShroomsCoreImpl::agentsFind(const std::string& peers)
{
    hub().find(peers);
    return hub().found();
}

std::string ShroomsCoreImpl::agentWatch(const std::string& address, const std::string& session, const std::string& tail)
{
    if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
    if (!safeSession(session)) return errorJson("not a session name", session);
    hub().watch(address, session, std::atoi(tail.c_str()));
    return "{\"ok\":true}";
}

std::string ShroomsCoreImpl::agentEvents(const std::string& after)
{
    return hub().events(std::atoll(after.c_str()));
}

std::string ShroomsCoreImpl::agentGet(const std::string& address, const std::string& path)
{
    if (!agents::safePath(path)) return errorJson("not an agent path", path);
    std::string out, err;
    if (!agents::request(address, "GET", path, "", 5, out, err)) return errorJson("agent", err);
    return out;
}

std::string ShroomsCoreImpl::agentPost(const std::string& address, const std::string& path, const std::string& body)
{
    if (!agents::safePath(path)) return errorJson("not an agent path", path);
    std::string out, err;
    if (!agents::request(address, "POST", path, body, 8, out, err)) return errorJson("agent", err);
    return out.empty() ? "{\"ok\":true}" : out;
}

std::string ShroomsCoreImpl::agentUpload(const std::string& address, const std::string& session, const std::string& localPath)
{
    if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
    if (!safeSession(session)) return errorJson("not a session name", session);
    return "{\"job\":" + std::to_string(hub().upload(address, session, localPath)) + "}";
}

std::string ShroomsCoreImpl::agentVoice(const std::string& action)
{
    if (action == "setup") hub().voiceSetup();
    else if (action == "remove") hub().voiceRemove();
    else if (action != "state") return errorJson("unknown action", action);
    return hub().voiceState();
}

std::string ShroomsCoreImpl::agentSpeak(const std::string& action, const std::string& text, const std::string& lang)
{
    if (action == "say") {
        if (text.size() > 200 * 1024) return errorJson("too long to read", std::to_string(text.size()) + " bytes");
        std::string why = hub().speak(text, lang == "cs");
        return why.empty() ? "{\"ok\":true}" : errorJson("cannot read it aloud", why);
    }
    if (action == "stop") {
        hub().speakStop();
        return "{\"ok\":true}";
    }
    if (action != "state") return errorJson("unknown action", action);
    std::string engine;
    bool on = hub().speaking(engine);
    return std::string("{\"speaking\":") + (on ? "true" : "false") + ",\"engine\":\"" + jsonEscape(engine) + "\"}";
}

std::string ShroomsCoreImpl::agentRecord(const std::string& action, const std::string& address, const std::string& session, const std::string& lang)
{
    if (action == "start") {
        std::string why = hub().recordStart();
        return why.empty() ? "{\"ok\":true}" : errorJson("cannot record", why);
    }
    if (action == "cancel") {
        hub().recordCancel();
        return "{\"ok\":true}";
    }
    if (action == "send") {
        // The recording as a voice note: queued, sent, transcribed on the
        // agent's machine and sent as the turn — nothing comes back here.
        if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
        if (!safeSession(session)) return errorJson("not a session name", session);
        std::string err;
        std::string id = hub().recordSend(address, session, err);
        if (id.empty()) return errorJson("cannot send it", err);
        return "{\"id\":\"" + id + "\"}";
    }
    if (action != "stop") return errorJson("unknown action", action);
    if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
    if (!safeSession(session)) return errorJson("not a session name", session);
    std::string err;
    long id = hub().recordStop(address, session, lang.empty() ? "auto" : lang, err);
    if (id < 0) return errorJson("cannot stop", err);
    return "{\"job\":" + std::to_string(id) + "}";
}

std::string ShroomsCoreImpl::agentJobs()
{
    return hub().jobs();
}

std::string ShroomsCoreImpl::agentQueue(const std::string& address, const std::string& session, const std::string& text)
{
    if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
    if (!safeSession(session)) return errorJson("not a session name", session);
    if (text.find_first_not_of(" \t\r\n") == std::string::npos) return errorJson("an empty message", "");
    return "{\"id\":\"" + hub().queueText(address, session, text) + "\"}";
}

namespace {
std::vector<std::string> lines(const std::string& s)
{
    std::vector<std::string> out;
    std::stringstream in(s);
    std::string l;
    while (std::getline(in, l))
        if (!l.empty()) out.push_back(l);
    return out;
}
}  // namespace

std::string ShroomsCoreImpl::agentQueueFiles(const std::string& address, const std::string& session,
                                             const std::string& text, const std::string& files)
{
    if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
    if (!safeSession(session)) return errorJson("not a session name", session);
    auto fs = lines(files);
    if (fs.empty() && text.find_first_not_of(" \t\r\n") == std::string::npos) return errorJson("an empty message", "");
    // Only what agentKeep or agentPaste kept: a view names no other file.
    std::string dir = hub().keptDir() + "/";
    for (const auto& f : fs)
        if (f.compare(0, dir.size(), dir) != 0 || f.find("/..") != std::string::npos) return errorJson("not a kept file", f);
    return "{\"id\":\"" + hub().queueText(address, session, text, fs) + "\"}";
}

std::string ShroomsCoreImpl::agentKeep(const std::string& localPath)
{
    std::string err;
    std::string file = hub().keepFile(localPath, err);
    if (file.empty()) return errorJson("cannot attach it", err);
    return "{\"file\":" + jsonString(file) + "}";
}

std::string ShroomsCoreImpl::agentGather(const std::string& addresses, const std::string& path)
{
    if (!agents::safePath(path)) return errorJson("not an agent path", path);
    auto as = lines(addresses);
    for (const auto& a : as)
        if (!agents::isMeshAddress(a)) return errorJson("not a mesh address", a);
    return "{\"gather\":" + std::to_string(hub().gather(as, path)) + "}";
}

std::string ShroomsCoreImpl::agentGathered()
{
    return hub().gathered();
}

std::string ShroomsCoreImpl::agentOutbox()
{
    return hub().outbox();
}

std::string ShroomsCoreImpl::agentUnqueue(const std::string& id)
{
    return hub().unqueue(id) ? "{\"ok\":true}" : errorJson("not queued", id);
}

std::string ShroomsCoreImpl::agentSearch(const std::string& address, const std::string& session, const std::string& query)
{
    if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
    if (!safeSession(session)) return errorJson("not a session name", session);
    return "{\"search\":" + std::to_string(hub().search(address, session, query)) + "}";
}

std::string ShroomsCoreImpl::agentSearched()
{
    return hub().searched();
}

std::string ShroomsCoreImpl::agentOpenUrl(const std::string& url)
{
    std::string err;
    if (!hub().openUrl(url, err)) return errorJson("cannot open it", err);
    return "{\"ok\":true}";
}

std::string ShroomsCoreImpl::agentPaste(const std::string&, const std::string&)
{
    std::string err;
    std::string file = hub().pasteImage(err);
    if (!err.empty()) return errorJson("cannot paste", err);
    if (file.empty()) return "{\"none\":true}";
    return "{\"file\":" + jsonString(file) + "}";
}

std::string ShroomsCoreImpl::agentMoveKept(const std::string& address, const std::string& from, const std::string& to)
{
    if (!agents::isMeshAddress(address)) return errorJson("not a mesh address", address);
    if (!safeSession(from) || !safeSession(to)) return errorJson("not a session name", from + " → " + to);
    hub().renamed(address, from, to);
    return "{\"ok\":true}";
}

std::string ShroomsCoreImpl::agentDelete(const std::string& address, const std::string& path)
{
    if (!agents::safePath(path)) return errorJson("not an agent path", path);
    std::string out, err;
    if (!agents::request(address, "DELETE", path, "", 8, out, err)) return errorJson("agent", err);
    const std::string sessions = "/v1/sessions/";
    if (path.compare(0, sessions.size(), sessions) == 0 && path.find('/', sessions.size()) == std::string::npos) {
        agents::Hub::forgetHistory(address, path.substr(sessions.size()));
    }
    return "{\"ok\":true}";
}
