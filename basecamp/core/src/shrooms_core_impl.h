#pragma once

#include <string>

#include "logos_module_context.h"

/**
 * @brief Reads a Shrooms daemon's status and hands it to the view.
 *
 * This module exists because of a boundary, not because the work is hard. A
 * `ui_qml` app runs inside Basecamp's QML sandbox: a deny-all network manager
 * blocks every outgoing request, and a URL interceptor resolves local files
 * only under the app's own install directory. So the view can reach neither the
 * daemon's control socket nor a status file in /run, however either is
 * permissioned. A Logos module runs in its own process and is not sandboxed,
 * which makes it the supported way across — Basecamp's own spec says UI apps
 * reach the outside "indirectly through Logos Modules".
 *
 * The view calls this synchronously:
 *
 *     logos.callModule("shrooms_core", "status", [])
 *
 * Read-only by construction. Nothing here changes a mesh, so a bug in it cannot
 * take one down — the worst it can do is show nothing.
 */
class ShroomsCoreImpl : public LogosModuleContext
{
public:
    /**
     * The daemon's status as a JSON document, or a JSON object with an
     * `error` field explaining why there is none.
     *
     * An error is returned rather than an empty string so the view can say what
     * is wrong. "Nothing appeared" is the failure that wasted a day on this
     * feature already.
     */
    std::string status();

    /**
     * As status(), but reading a specific control socket.
     *
     * For a machine running more than one daemon, and for tests, which need a
     * socket that is not the system one.
     */
    std::string statusFrom(const std::string& socketPath);
};
