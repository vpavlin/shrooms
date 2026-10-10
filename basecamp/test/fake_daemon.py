"""A stand-in Shrooms daemon for core_check: answers on a unix socket with
what it was asked, so the test can see the exact request the core built.
/join takes three seconds, like a join waiting for the far side; a path
ending in /refuse answers 400 the way the daemon refuses a setting."""
import json, os, socketserver, sys, time
from http.server import BaseHTTPRequestHandler

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def answer(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n).decode() if n else ""
        if self.path == "/join":
            time.sleep(3)
        code = 400 if self.path.endswith("/refuse") else 200
        out = json.dumps({"method": self.command, "path": self.path, "body": body}).encode()
        if code != 200:
            out = b"no mesh called \"nope\""
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    do_GET = do_POST = answer

    def log_message(self, *a):
        pass

    def address_string(self):
        return "unix"

class S(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

path = sys.argv[1]
if os.path.exists(path):
    os.unlink(path)
S(path, H).serve_forever()
