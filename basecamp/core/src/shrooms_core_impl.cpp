#include "shrooms_core_impl.h"

#include <cctype>
#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <map>
#include <string>
#include <vector>

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
const char* kSocket = "/run/shrooms/shrooms.sock";
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
                               std::string& err)
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
    timeval tv{};
    tv.tv_sec = 2;
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
    ::mkdir(base.c_str(), 0700);
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
