// Drives the core's daemon calls against a stand-in daemon (fake_daemon.py)
// and checks the request each one sends. Run by test/core_check.sh.
//
// The view's write paths had no test at all, and that is how a services form
// shipped that deleted configured services and could not address a mesh.
#include "../core/src/shrooms_core_impl.h"

#include <chrono>
#include <cstdlib>
#include <fstream>
#include <sys/stat.h>
#include <cstdio>
#include <string>
#include <thread>

static int fails = 0;
#define CHECK(c, ...) do { if (!(c)) { std::printf("FAIL %s: ", #c); std::printf(__VA_ARGS__); std::printf("\n"); fails++; } else std::printf("ok   %s\n", #c); } while (0)

static bool has(const std::string& s, const std::string& want) { return s.find(want) != std::string::npos; }

int main()
{
    ShroomsCoreImpl core;

    std::string r = core.servicesOf("office");
    CHECK(has(r, "\"path\": \"/config/services?mesh=office\"") && has(r, "\"GET\""), "%s", r.c_str());
    r = core.servicesOf("");
    CHECK(has(r, "\"path\": \"/config/services\""), "%s", r.c_str());
    r = core.servicesOf("of fice&x=1");
    CHECK(has(r, "\"error\"") && !has(r, "\"path\""), "a label that is not one reached the daemon: %s", r.c_str());

    r = core.setServicesOf("office", " immich:2283 , jellyfin:8096,");
    CHECK(has(r, R"({\"label\":\"office\",\"services\":[\"immich:2283\",\"jellyfin:8096\"]})"), "%s", r.c_str());

    r = core.setAnnounceRevocations("home", false);
    CHECK(has(r, "/config/announce-revocations") && has(r, R"({\"label\":\"home\",\"enabled\":false})"), "%s", r.c_str());

    r = core.blindRelays();
    CHECK(has(r, "\"path\": \"/config/blind-relays\"") && has(r, "\"GET\""), "%s", r.c_str());
    r = core.setBlindRelays("", "203.0.113.10:31760");
    CHECK(has(r, R"({\"label\":\"\",\"relays\":\"203.0.113.10:31760\"})"), "the token must not be sent: %s", r.c_str());
    r = core.setBlindRelaysWithToken("home", "none", "t\"k");
    CHECK(has(r, R"(\"token\":\"t\\\"k\")"), "%s", r.c_str());

    // A refusal carries the daemon's own sentence.
    r = core.setName("x");
    CHECK(has(r, "/config/name"), "%s", r.c_str());

    // A join returns at once and reports when the far side answers.
    auto t0 = std::chrono::steady_clock::now();
    r = core.joinWithInviteStart("tok", "laptop", "home");
    auto took = std::chrono::steady_clock::now() - t0;
    CHECK(has(r, "\"started\":true"), "%s", r.c_str());
    CHECK(took < std::chrono::milliseconds(500), "start blocked the caller");
    r = core.joinWithInviteStart("tok", "laptop", "home");
    CHECK(has(r, "already running"), "a second join was let through: %s", r.c_str());
    r = core.joinProgress();
    CHECK(has(r, "\"running\":true"), "%s", r.c_str());
    for (int i = 0; i < 80 && !has(r, "\"done\""); i++) {
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
        r = core.joinProgress();
    }
    CHECK(has(r, "\"done\":true") && has(r, "\"path\": \"/join\"")
          && has(r, R"(\"token\":\"tok\",\"name\":\"laptop\",\"label\":\"home\")"), "%s", r.c_str());
    // Past the two seconds everything else gives up after.
    took = std::chrono::steady_clock::now() - t0;
    CHECK(took > std::chrono::milliseconds(2900), "the join did not wait for the far side");

    // Inviting with a card (ADR-050): the core carries requests; the daemon
    // knows the formats.
    r = core.inviteNew("office");
    CHECK(has(r, "/invite/new") && has(r, R"({\"mesh\":\"office\"})"), "%s", r.c_str());
    r = core.inviteHoldStart("tok", "office");
    CHECK(has(r, "\"started\":true"), "%s", r.c_str());
    r = core.inviteHoldProgress();
    CHECK(has(r, "\"running\":true"), "%s", r.c_str());
    // A second hold supersedes the first rather than being refused.
    r = core.inviteHoldStart("tok2", "office");
    CHECK(has(r, "\"started\":true"), "%s", r.c_str());
    for (int i = 0; i < 40 && !has(r, "\"done\""); i++) {
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
        r = core.inviteHoldProgress();
    }
    CHECK(has(r, "\"done\":true") && has(r, "tok2") && !has(r, "\"tok\""), "the superseded hold answered: %s", r.c_str());
    core.inviteHoldCancel();
    r = core.inviteHoldProgress();
    CHECK(has(r, "\"idle\":true"), "%s", r.c_str());

    r = core.inviteDraft("office", "aa", "bb", "cc", "kitchen \"pi\"");
    CHECK(has(r, "/invite/draft") && has(r, R"(\"name\":\"kitchen \\\"pi\\\"\")"), "%s", r.c_str());
    r = core.inviteReply("tok", "ee", "pi", "office", "RFJBRlQ=", "3045");
    CHECK(has(r, "/invite/reply") && has(r, R"(\"credential\":\"RFJBRlQ=\",\"signature\":\"3045\")"), "%s", r.c_str());
    r = core.inviteReply("tok", "ee", "pi", "office", "", "");
    CHECK(has(r, "/invite/reply") && !has(r, "credential") && !has(r, "signature"),
          "a mesh with no authority replies with nothing to verify: %s", r.c_str());

    // The card's account for a mesh, from the admin files, matched by key.
    {
        char tmpl[] = "/tmp/core_check_XXXXXX";
        std::string home = mkdtemp(tmpl);
        setenv("XDG_CONFIG_HOME", home.c_str(), 1);
        mkdir((home + "/shrooms").c_str(), 0700);
        std::ofstream(home + "/shrooms/admin-office.json") << R"({"priv":"","keys":["AKEYOFFICE"],"account":3})";
        std::ofstream(home + "/shrooms/admin-home.json") << R"({"priv":"x","keys":["AKEYHOME"]})";
        r = core.cardPath("ZZZ,AKEYOFFICE");
        CHECK(has(r, "m/64265'/3'/0'") && has(r, "\"known\":true"), "%s", r.c_str());
        r = core.cardPath("AKEYHOME");
        CHECK(has(r, "m/64265'/0'/0'") && has(r, "\"known\":true"), "an absent account is 0: %s", r.c_str());
        r = core.cardPath("NOBODY");
        CHECK(has(r, "m/64265'/0'/0'") && has(r, "\"known\":false"), "%s", r.c_str());
        r = core.cardPath("\"account\":9");
        CHECK(has(r, "\"known\":false"), "a key that is not one matched: %s", r.c_str());
    }

    std::printf(fails ? "%d failed\n" : "all passed\n", fails);
    return fails ? 1 : 0;
}
