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
     * @brief The services this node has CONFIGURED, as JSON.
     *
     * Not the ones it is running, which is what status() reports and what the
     * status file carries. The two differ: a service that is switched off, that
     * failed to bind, or that was added since the last reload is absent from
     * the running list and present here.
     *
     * That distinction is the reason this exists. setServices() replaces the
     * whole list, so anything editing one service has to send the others back
     * unchanged — and reading them from the running list would delete every
     * configured service that happened not to be up, silently, as the ordinary
     * result of adding an unrelated one. Read with this before writing.
     *
     * Needs a daemon that answers GET on /config/services; an older one
     * refuses with "POST a setting".
     */
    std::string services();

    /** @brief As services(), against a specific control socket. */
    std::string servicesFrom(const std::string& socketPath);

    /**
     * @brief Replaces the list of services this node advertises.
     *
     * REPLACES. To change one service, read services() — not status() — and
     * send the rest back with it. See services() for why that matters.
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
    std::string setAnnounceServices(const std::string& label, bool on);
    std::string setAnnounceServicesOn(const std::string& socketPath,
                                      const std::string& label, bool on);

    /**
     * @brief Whether this device forwards for peers of one mesh (ADR-013).
     *
     * @param label The mesh, as it appears in status(). Empty means the first
     * one, whose settings live at the top level of the config.
     *
     * Per mesh, because carrying traffic for your own machines and for
     * somebody else's are different decisions.
     */
    std::string setRelay(const std::string& label, bool on);
    std::string setRelayOn(const std::string& socketPath, const std::string& label, bool on);

    /**
     * @brief Whether the router is asked to open this node's port (ADR-024).
     *
     * Global rather than per mesh: it is one UDP port and one request.
     */
    std::string setPortMapping(bool on);
    std::string setPortMappingOn(const std::string& socketPath, bool on);

    /**
     * @brief Whether peers are told which ports are bound here (ADR-026).
     *
     * Separate from setAnnounceServices() and separately off by default: those
     * are declared by somebody who meant it, these are whatever is listening.
     */
    std::string setAnnounceBound(const std::string& label, bool on);
    std::string setAnnounceBoundOn(const std::string& socketPath,
                                   const std::string& label, bool on);

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

    /*
     * Minting an invite is deliberately absent, and this is where somebody
     * will come looking for it.
     *
     * An invite is two halves (ADR-017): the daemon holds the exchange open,
     * and the admin key signs the credential that comes out of it. The daemon
     * has never held that key — it is a passphrase-protected file in a user's
     * session — and that separation is exactly what makes handing this socket
     * to a group a bounded grant rather than a way to admit anybody to the
     * mesh. Teaching this module to sign would remove the property the tier is
     * built on.
     *
     * There was a startInvite() here that posted to /invite/new. No daemon has
     * ever served that path, so every call returned an error; it is gone
     * rather than left to look like a feature. A view that wants to offer this
     * should print the command instead.
     */

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
     * @brief Reads a small view preference, or "" when it has never been set.
     *
     * The view has no storage of its own. A QML app in Basecamp's sandbox
     * cannot write a file, and the daemon's config is the wrong place for
     * "should the graph draw inferred links" — that is a property of this
     * window, not of the mesh, and putting it there would mean every node's
     * config carrying somebody's display choices.
     *
     * This module is not sandboxed, so it keeps them: one small file of
     * key=value lines beside the module's own data. Nothing here is secret and
     * nothing here affects the mesh; the worst a corrupt file can do is show a
     * toggle in the wrong position.
     *
     * @param key A short identifier. Anything outside [A-Za-z0-9_.-] is
     * rejected rather than escaped, because these are written by this view and
     * a key that needs escaping is a bug rather than a user's input.
     */
    std::string getPref(const std::string& key);

    /** @brief Writes a view preference. An empty value removes it. */
    std::string setPref(const std::string& key, const std::string& value);

    /**
     * @brief The daemon's recent log, as `{"lines":[{"t":…,"level":…,"msg":…}]}`.
     *
     * The desktop equivalent of the app's log pane. Basecamp cannot read the
     * journal — it cannot read a file at all — so without this the answer to
     * "why is nothing connecting" is a terminal.
     *
     * @param sinceMs Return only lines newer than this stamp, as taken from
     * the `t` of the newest line already shown. Empty or "0" returns
     * everything held. A poller that always asks for everything re-renders two
     * hundred lines every couple of seconds, which is visible.
     *
     * A string, not an integer, and that is not fussiness: the stamp is unix
     * milliseconds, which passed 2^31 in 2001. A bridge that marshals numbers
     * as 32-bit ints would truncate it into a value from some other decade,
     * and the failure would be a log pane that silently shows everything or
     * nothing. Digits in a string cross any bridge intact.
     */
    std::string logs(const std::string& sinceMs);

    /** @brief As logs(), against a specific control socket. */
    std::string logsFrom(const std::string& socketPath, const std::string& sinceMs);

    /**
     * @brief Restarts the daemon, applying the settings that need one.
     *
     * The other half of every setting whose result says "on the next restart":
     * the mode, a mesh being switched on or off, a mesh just joined. Without
     * it those settings are written and then need a terminal, which is the
     * terminal these controls exist to avoid.
     *
     * The daemon refuses when nothing would start it again — run from a shell
     * rather than under a service manager — because exiting there is a mesh
     * that stays down. Its refusal is returned as-is: it explains itself
     * better than a caller could.
     */
    std::string restart();

    /** @brief As restart(), against a specific control socket. */
    std::string restartOn(const std::string& socketPath);

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
