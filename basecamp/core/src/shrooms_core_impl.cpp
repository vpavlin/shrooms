#include "shrooms_core_impl.h"

#include <cerrno>
#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

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
        default:
            if (static_cast<unsigned char>(c) < 0x20) {
                char buf[7];
                std::snprintf(buf, sizeof(buf), "\\u%04x", c);
                out += buf;
            } else {
                out += c;
            }
        }
    }
    return out;
}

std::string errorJson(const std::string& what, const std::string& detail)
{
    return "{\"error\":\"" + jsonEscape(what) + "\",\"detail\":\"" +
           jsonEscape(detail) + "\"}";
}

/**
 * One HTTP GET over a unix socket, returning the response body.
 *
 * Hand-rolled rather than pulled in: the daemon speaks HTTP/1.1 over
 * AF_UNIX and this needs exactly one request with no keep-alive, no
 * redirects and no TLS. A dependency for that would be larger than the
 * problem.
 */
bool httpGetUnix(const std::string& path, const std::string& target,
                 std::string& body, std::string& err)
{
    int fd = ::socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) {
        err = std::string("socket: ") + std::strerror(errno);
        return false;
    }

    sockaddr_un addr{};
    addr.sun_family = AF_UNIX;
    if (path.size() >= sizeof(addr.sun_path)) {
        ::close(fd);
        err = "socket path is too long";
        return false;
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
        return false;
    }

    const std::string req =
        "GET " + target + " HTTP/1.0\r\nHost: unix\r\nConnection: close\r\n\r\n";
    size_t sent = 0;
    while (sent < req.size()) {
        ssize_t n = ::write(fd, req.data() + sent, req.size() - sent);
        if (n <= 0) {
            err = std::string("write: ") + std::strerror(errno);
            ::close(fd);
            return false;
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
            return false;
        }
        if (n == 0) break;
        raw.append(buf, static_cast<size_t>(n));
        if (raw.size() > kMaxResponse) {
            err = "response is implausibly large";
            ::close(fd);
            return false;
        }
    }
    ::close(fd);

    const auto sep = raw.find("\r\n\r\n");
    if (sep == std::string::npos) {
        err = "no HTTP header terminator in the reply";
        return false;
    }
    // The status line, checked rather than assumed: a 404 body is not status,
    // and returning it would put a decoding error in front of the user instead
    // of the real one.
    if (raw.compare(0, 9, "HTTP/1.1 ") != 0 && raw.compare(0, 9, "HTTP/1.0 ") != 0) {
        err = "the reply is not HTTP";
        return false;
    }
    if (raw.compare(9, 3, "200") != 0) {
        err = "the daemon answered " + raw.substr(9, 3);
        return false;
    }

    body = raw.substr(sep + 4);
    return true;
}

} // namespace

std::string ShroomsCoreImpl::statusFrom(const std::string& socketPath)
{
    std::string body, err;
    if (httpGetUnix(socketPath, "/status", body, err)) {
        return body;
    }
    return errorJson("cannot read the Shrooms daemon", err);
}

std::string ShroomsCoreImpl::status()
{
    std::string body, err, firstErr;

    for (const char* path : {kSocket, kLegacySocket}) {
        if (httpGetUnix(path, "/status", body, err)) {
            return body;
        }
        if (firstErr.empty()) firstErr = err;
    }

    // Permission is the failure worth naming, because it has a one-line fix and
    // looks identical to "the daemon is not running" from here. The daemon
    // needs CAP_NET_ADMIN and so runs as root; its socket is 0660, and
    // Basecamp is not root.
    std::string hint = firstErr;
    if (firstErr.find("Permission denied") != std::string::npos) {
        hint += " — set socket_group in the daemon's config to a group you are in";
    }
    return errorJson("cannot read the Shrooms daemon", hint);
}
