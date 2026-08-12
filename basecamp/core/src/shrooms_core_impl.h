#pragma once

#include <string>

#include "logos_module_context.h"

/**
 * @brief Talks to a Shrooms daemon over its control socket on behalf of the view.
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
 *     logos.callModule("shrooms_core", "setName", ["kitchen-pi"])
 *
 * This was read-only by construction until the write endpoints landed, and the
 * safety that bought is worth stating plainly now that it is gone: a bug here
 * can take a mesh down. Every method below therefore decides nothing on its
 * own. It marshals arguments into one small fixed-shape request and returns
 * whatever the daemon says. Validation, ordering and rollback belong to the
 * daemon, which is the only party that can see the whole node.
 */
class ShroomsCoreImpl : public LogosModuleContext
{
public:
    /**
     * @brief The daemon's status as a JSON document, or a JSON object with an
     * `error` field explaining why there is none.
     *
     * An error is returned rather than an empty string so the view can say what
     * is wrong. "Nothing appeared" is the failure that wasted a day on this
     * feature already.
     */
    std::string status();

    /**
     * @brief As status(), but reading a specific control socket.
     *
     * For a machine running more than one daemon, and for tests, which need a
     * socket that is not the system one.
     *
     * Every write method below has the same pairing, for the same reason.
     */
    std::string statusFrom(const std::string& socketPath);

    /**
     * @brief Renames this node, as the rest of the mesh sees it.
     *
     * @param name The new device name. This is free-form user input straight
     * out of a text field and is escaped, not filtered — a name containing a
     * quote must reach the daemon intact rather than truncate the request into
     * something that parses as a different one.
     */
    std::string setName(const std::string& name);

    /** @brief As setName(), against a specific control socket. */
    std::string setNameOn(const std::string& socketPath, const std::string& name);

    /**
     * @brief Switches the node between "Core" and "Edge".
     *
     * @param mode Passed through unchecked. The set of modes is the daemon's to
     * define, and a copy of it here would be a second place to forget to update
     * — the daemon rejects what it does not know and its refusal is returned.
     */
    std::string setMode(const std::string& mode);

    /** @brief As setMode(), against a specific control socket. */
    std::string setModeOn(const std::string& socketPath, const std::string& mode);

    /**
     * @brief Replaces the list of services this node advertises.
     *
     * @param csv Comma-separated `name:port` specs, e.g. `immich:2283,jellyfin:8096`.
     * Taken as one string because that is what a single text field yields; it is
     * split here so the daemon gets the array its API is defined in terms of.
     *
     * The list replaces rather than adds, so an empty string clears every
     * advertised service. That is a real thing to want and not an accident to
     * guard against.
     */
    std::string setServices(const std::string& csv);

    /** @brief As setServices(), against a specific control socket. */
    std::string setServicesOn(const std::string& socketPath, const std::string& csv);

    /**
     * @brief Tells this device's peers what it publishes, or stops telling them.
     *
     * The one setting here that changes what other people can see rather than
     * what this device does. Off is the default and is the security property:
     * a mesh shared with other people discloses nothing until somebody decides
     * otherwise.
     */
    std::string setAnnounceServices(bool on);
    std::string setAnnounceServicesOn(const std::string& socketPath, bool on);

    /**
     * @brief Turns one joined mesh on or off without leaving it.
     *
     * Disabling keeps the credentials, so this is the reversible half of
     * leaveMesh() and the one a user should reach for first.
     *
     * @param label The mesh's label, as it appears in status().
     * @param enabled Whether the mesh should carry traffic.
     */
    std::string setMeshEnabled(const std::string& label, bool enabled);

    /** @brief As setMeshEnabled(), against a specific control socket. */
    std::string setMeshEnabledOn(const std::string& socketPath,
                                 const std::string& label, bool enabled);

    /**
     * @brief Mints an invite for someone else to join a mesh this node is in.
     *
     * @param name A name for the invitee, recorded with the invite.
     * @return The daemon's `{"token":"...","expires":"..."}` on success. The
     * token is the secret that admits a new node, so the view must treat this
     * body as sensitive: it is returned, not logged.
     */
    std::string startInvite(const std::string& name);

    /** @brief As startInvite(), against a specific control socket. */
    std::string startInviteOn(const std::string& socketPath, const std::string& name);

    /**
     * @brief Joins a mesh using a token minted by startInvite() elsewhere.
     *
     * @param token The invite token, as handed over out of band.
     * @param name This node's name within the mesh being joined.
     * @param label The local label to file the mesh under.
     *
     * This is the slowest call here — it involves the far side — and it is
     * subject to the same read timeout as everything else, so a join that has
     * not answered in time reports a timeout even though it may yet succeed.
     * The cure is to call status() and look, not to call this again.
     */
    std::string joinWithInvite(const std::string& token, const std::string& name,
                               const std::string& label);

    /** @brief As joinWithInvite(), against a specific control socket. */
    std::string joinWithInviteOn(const std::string& socketPath, const std::string& token,
                                 const std::string& name, const std::string& label);

    /**
     * @brief Leaves a mesh and discards its credentials.
     *
     * Irreversible without a fresh invite, unlike setMeshEnabled(label, false).
     * A view offering this should say so.
     *
     * @param label The mesh's label, as it appears in status().
     */
    std::string leaveMesh(const std::string& label);

    /** @brief As leaveMesh(), against a specific control socket. */
    std::string leaveMeshOn(const std::string& socketPath, const std::string& label);

    /**
     * @brief Makes the daemon re-read its configuration and reconcile.
     *
     * The other writes already take effect, so this is for changes made behind
     * this module's back — a hand-edited config file — and for shaking loose a
     * node whose state has drifted from what it was told.
     */
    std::string reload();

    /** @brief As reload(), against a specific control socket. */
    std::string reloadOn(const std::string& socketPath);
};
