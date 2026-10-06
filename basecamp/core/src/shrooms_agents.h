#pragma once

#include <atomic>
#include <map>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

/**
 * Agents on the mesh (docs/agents.md), for the Basecamp view.
 *
 * The view runs in Basecamp's QML sandbox, which blocks all network access,
 * and reaches the outside through synchronous calls into this module. A slow
 * call freezes the whole view, so the network is kept off that path: finding
 * agents and following a session's live stream happen on threads of their own
 * here, and the view's calls only read what they have collected. Sending a
 * message, answering a prompt and changing a setting are single requests to a
 * machine on the mesh, bounded by a short timeout.
 *
 * Only mesh addresses are ever dialled (ULA fd00::/8 and the 198.18.0.0/15
 * IPv4 aliases), on the agent port: the connections are plain HTTP and rely
 * on the WireGuard tunnel for encryption and for who can reach an agent.
 *
 * Qt-free and ASCII-only, as the module glue requires.
 */
namespace agents {

constexpr int kPort = 7387;

/**
 * Whether a path may be sent to an agent: its API only, printable, no spaces
 * and no way to step out of it.
 */
bool safePath(const std::string& path);

/** A string field of a small JSON reply, with its escapes decoded. */
std::string field(const std::string& json, const std::string& key);

/** Whether an address is a literal mesh address. */
/**
 * The environment for programs this starts, without the AppImage's loader
 * settings (LD_PRELOAD, LD_LIBRARY_PATH, …). ptrs is the envp, valid while the
 * value lives.
 */
struct ChildEnv {
    std::vector<std::string> vars;
    std::vector<char*> ptrs;
};
ChildEnv childEnv();

/** Whether a link is one to open: http or https, printable, not huge. */
bool safeUrl(const std::string& url);

/** A JSON event with every string longer than max bytes cut, still valid JSON. */
std::string trimStrings(const std::string& ev, size_t max);
bool isMeshAddress(const std::string& address);

/**
 * Makes a directory and every missing parent, like mkdir -p; true when it
 * exists afterwards. A plain mkdir per level failed on a fresh machine with no
 * ~/.local yet, and with it everything kept under it.
 */
bool makeDirs(const std::string& path, unsigned mode = 0700);

/**
 * One plain HTTP request to an agent. Returns true on a 2xx, with the body;
 * otherwise false and why, including the agent's own error text.
 */
bool request(const std::string& address, const std::string& method, const std::string& target,
             const std::string& body, int timeoutSec, std::string& out, std::string& err);

class Hub {
public:
    ~Hub();

    /**
     * Probes the given peers for agents in the background. `peers` is
     * "name|mesh|address" entries separated by ";". A round already running
     * is not started again.
     */
    void find(const std::string& peers);

    /** What the last rounds found, as a JSON array. */
    std::string found();

    /**
     * Follows one session's live stream, replacing whatever was followed.
     * tail > 0 starts at the last `tail` events instead of the first.
     */
    void watch(const std::string& address, const std::string& session, int tail);

    /**
     * The watched session's events after a local index, as
     * {"next":N,"connected":bool,"error":"...","kept":MS,"epoch":E,"events":[...]}.
     * Events are the agent's own JSON, verbatim; "partial" ones carry
     * streamed reply text.
     *
     * Opened at its end, the events start with the end of the conversation
     * as it was last seen here (kept on disk, History below), and only what
     * came after it is asked of the machine; until the machine answers,
     * "kept" is when the copy was made, in epoch milliseconds, then 0.
     * "epoch" is for a view to notice the events were replaced; nothing
     * replaces them today.
     */
    std::string events(long long after);

    /** Where what is kept of a session is, on disk. */
    static std::string historyPath(const std::string& address, const std::string& session);
    /** Drops what is kept on disk of a session: it was deleted. */
    static void forgetHistory(const std::string& address, const std::string& session);

    /**
     * Sends a local file to a session's machine in the background (an agent
     * keeps it and returns its path there). Returns the job's id.
     */
    long upload(const std::string& address, const std::string& session, const std::string& localPath);

    /**
     * Starts recording a voice note from the default microphone, with the
     * system's own recorder (pw-record, else parecord, else arecord): Basecamp
     * ships no Qt Multimedia, and a view that imports a missing module does
     * not load at all. Returns an empty string or why it could not start.
     */
    std::string recordStart();

    /**
     * Reads text aloud on this machine, stopping whatever was being read:
     * Piper when it is set up (~/.local/share/shrooms/piper: the piper binary
     * and cs.onnx / en.onnx voices, each with its .onnx.json), otherwise
     * speech-dispatcher's spd-say. Basecamp ships no Qt TextToSpeech, as it
     * ships no Qt Multimedia. Returns "" or why it could not.
     */
    std::string speak(const std::string& text, bool czech);

    /**
     * The natural voice, set up with one click rather than by hand: Piper
     * (rhasspy/piper's standalone build, for this machine's architecture) and
     * the en_US-lessac-medium voice, downloaded in the background into
     * ~/.local/share/shrooms/piper. voiceState() says whether it is there,
     * what is happening and why it failed: {"installed","busy","step","error",
     * "engine"}. voiceRemove() deletes it, back to spd-say.
     */
    void voiceSetup();
    void voiceRemove();
    std::string voiceState();
    void speakStop();
    /** Whether something is being read, and with which engine. */
    bool speaking(std::string& engine);

    /**
     * Stops recording and, in the background, sends the note to be
     * transcribed on that machine (whisper.cpp, docs/agents.md). Returns the
     * job's id, or -1 with why in err. cancel drops the recording.
     */
    long recordStop(const std::string& address, const std::string& session, const std::string& lang,
                    std::string& err);
    void recordCancel();

    /**
     * An image on the clipboard, attached like a file: Basecamp's QML can
     * paste text only. Read with wl-paste (Wayland) or xclip (X11) into the
     * outbox's folder. Returns where it is kept, "" when the clipboard holds
     * no image (the caller pastes its text as usual), or "" with why in err.
     */
    std::string pasteImage(std::string& err);

    /**
     * Asks several machines the same GET in the background, each on its own —
     * so one that cannot be reached holds up none of the others, nor the
     * window. Replaces any gathering still running; gathered() reports it.
     */
    long gather(const std::vector<std::string>& addresses, const std::string& path);

    /**
     * The latest gathering: {"id":N,"results":[{"address","done","error",
     * "body"}]}, body being the machine's JSON as it gave it (null until
     * done, or on error).
     */
    std::string gathered();

    /**
     * Background jobs, as {"recording":bool,"jobs":[{"id","kind","state",
     * "name","path","text","error"}]}. state is pending, done or failed.
     */
    std::string jobs();

    /**
     * Opens a web link in the desktop's browser (xdg-open). Basecamp's views
     * may not: their sandbox blocks every http and https URL, so a link in a
     * conversation went nowhere. Only http and https; false, with why, else.
     */
    bool openUrl(const std::string& url, std::string& err);

    /**
     * Searches a session's whole conversation in the background — on a long
     * one that takes a second or more — replacing any search still running.
     * Returns its id; searched() reports it.
     */
    long search(const std::string& address, const std::string& session, const std::string& query);

    /**
     * The latest search: {"id":N,"done":bool,"error":"…","found":[…]}, found
     * being the agent's answer as it gave it (null until done, or on error).
     */
    std::string searched();

    /**
     * The outbox: what is written to a session goes here and is sent from
     * here, by a thread of its own, every few seconds until the agent has it —
     * so it can be written with the machine unreachable or this one offline.
     * In order per session; kept on disk across restarts. Each carries an id
     * the agent takes once, so sending again is harmless.
     */
    std::string queueText(const std::string& address, const std::string& session, const std::string& text,
                          const std::vector<std::string>& files = {});

    /**
     * Keeps a file picked or pasted for the next message, beside the outbox:
     * attaching needs nothing from the agent's machine, which gets the file
     * with the message (queueText). Returns where it is kept, or "" with err.
     */
    std::string keepFile(const std::string& localPath, std::string& err);
    /** Where kept files are. */
    std::string keptDir() { return outboxDir(); }

    /**
     * Stops the recording and queues it as a voice note: the agent keeps it,
     * transcribes it and sends what was said. Returns its id, or "" with err.
     */
    std::string recordSend(const std::string& address, const std::string& session, std::string& err);

    /** The outbox, oldest first: [{id,address,session,kind,text,created,error}]. */
    std::string outbox();

    /** Takes one out of the outbox, with its recording. */
    bool unqueue(const std::string& id);

private:
    struct Attached {
        std::string file, name, sent;   // sent: the path on the agent's machine, once it has it
    };
    struct Outgoing {
        std::string id, address, session, kind, text, file;
        long long created = 0;
        std::string error;
        std::vector<Attached> files;    // uploaded, in order, just before the message
    };
    void sweepOutbox();  // with mu_ held
    void loadOutbox();   // with mu_ held
    void saveOutbox();   // with mu_ held
    void startSender();
    void sendLoop();
    std::string outboxDir();
    std::vector<Outgoing> outbox_;
    bool outboxLoaded_ = false;
    std::once_flag senderStarted_;
    std::thread sender_;
    std::atomic<bool> stopping_{false};

    std::thread voiceThread_;
    std::atomic<bool> voiceBusy_{false};
    std::atomic<int> voiceChild_{-1};     // the download running, killed on exit
    std::string voiceStep_, voiceError_;  // under mu_
    int run(const std::vector<std::string>& argv);

    struct Job {
        long id;
        std::string kind, state, name, path, text, error;
    };
    long addJob(const std::string& kind, const std::string& name);
    void finishJob(long id, bool ok, const std::string& path, const std::string& text, const std::string& error);
    void follow(std::string address, std::string session, int tail, long long after, unsigned generation);
    long long listedLastSeq(const std::string& address, const std::string& session); // with mu_ held
    void stopFollower();

    std::mutex mu_;
    std::map<std::string, std::string> found_;   // address -> host JSON
    std::atomic<bool> finding_{false};

    std::thread follower_;
    std::atomic<unsigned> generation_{0};
    std::atomic<int> followFd_{-1};
    std::vector<std::string> events_;
    long long base_ = 0;
    long long kept_ = 0;   // when the shown events were kept; 0 when live
    long long keptLast_ = 0;            // the newest kept event's seq
    unsigned epoch_ = 0;
    // The end of each watched conversation, kept on disk for when its machine
    // cannot be reached.
    void saveHistory(const std::string& address, const std::string& session);
    static std::string dataDir(const std::string& sub);
    bool connected_ = false;
    std::string error_;

    std::vector<Job> jobs_;
    long nextJob_ = 1;
    int recorder_ = -1;
    int speaker_ = -1;        // process group of the speech pipeline
    std::string speakEngine_;
    std::string recording_;

    struct Gathered {
        std::string address, body, error;
        bool done = false;
    };
    long gatherId_ = 0;
    std::vector<Gathered> gathered_;

    long searchId_ = 0;
    bool searchDone_ = true;
    std::string searchFound_ = "null", searchError_;
};

}  // namespace agents
