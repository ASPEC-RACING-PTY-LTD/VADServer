"""HTTP API (see internal/smart/worker.go for the client)."""

import hmac
import json
import logging
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from . import VERSION, codec, config
from .codec import BadRequest
from .registry import NotInstalled

log = logging.getLogger("vdsmart")


class Busy:
    """Counts requests being analysed and limits how many run at once."""

    def __init__(self, n: int):
        self.sem = threading.BoundedSemaphore(n)
        self.lock = threading.Lock()
        self.n = 0

    def __enter__(self):
        with self.lock:
            self.n += 1
        self.sem.acquire()

    def __exit__(self, *a):
        self.sem.release()
        with self.lock:
            self.n -= 1


class CPU:
    """CPU use of this process between status calls."""

    def __init__(self):
        self.last = (time.monotonic(), _cpu_seconds())

    def percent(self) -> float:
        now, cpu = time.monotonic(), _cpu_seconds()
        (t0, c0), self.last = self.last, (now, cpu)
        return round(100 * (cpu - c0) / max(now - t0, 1e-3), 1)


def _cpu_seconds() -> float:
    t = os.times()
    return t.user + t.system


def _meminfo():
    out = {}
    try:
        with open("/proc/meminfo") as f:
            for line in f:
                k, v = line.split(":", 1)
                out[k] = int(v.split()[0]) * 1024
    except OSError:
        pass
    return out.get("MemTotal", 0), out.get("MemAvailable", 0)


def _rss() -> int:
    try:
        with open("/proc/self/status") as f:
            for line in f:
                if line.startswith("VmRSS:"):
                    return int(line.split()[1]) * 1024
    except OSError:
        pass
    return 0


def _gpu() -> str:
    try:
        import torch

        if torch.cuda.is_available():
            return torch.cuda.get_device_name(0)
    except Exception:
        pass
    return ""


class App:
    def __init__(self, engine, token: str = None, allow_no_token: bool = None):
        self.engine = engine
        self.reg = engine.reg
        self.token = config.TOKEN if token is None else token
        self.allow_no_token = config.ALLOW_NO_TOKEN if allow_no_token is None else allow_no_token
        self.busy = Busy(config.MAX_JOBS)
        self.cpu = CPU()
        self._gpu = None

    def authorised(self, header: str) -> bool:
        if not self.token:
            return self.allow_no_token
        want = "Bearer " + self.token
        return hmac.compare_digest(header.encode(), want.encode())

    def status(self) -> dict:
        from .engine import device

        total, avail = _meminfo()
        if self._gpu is None:
            self._gpu = _gpu()
        out = {
            "version": VERSION,
            "device": device(),
            "threads": config.THREADS or os.cpu_count() or 1,
            "models": self.reg.status(),
            "busy": self.busy.n,
            "rss": _rss(),
            "cpu_percent": self.cpu.percent(),
            "mem_total": total,
            "mem_available": avail,
        }
        if self._gpu:
            out["gpu"] = self._gpu
        return out

    def handle(self, method: str, path: str, query: dict, body: bytes):
        """Returns (status, JSON value)."""
        q = {k: v[-1] for k, v in query.items()}
        if method == "GET" and path == "/v1/status":
            return 200, self.status()
        if method != "POST":
            return 404, {"error": "not found"}
        if path in ("/v1/models/install", "/v1/models/remove"):
            req = _json(body)
            names = req.get("models")
            if not isinstance(names, list) or not all(isinstance(n, str) for n in names):
                raise BadRequest("models must be a list of names")
            try:
                if path.endswith("install"):
                    return 202, {"started": self.reg.install(names)}
                self.reg.remove(names)
                return 200, {"removed": names}
            except KeyError as e:
                raise BadRequest(f"unknown model {e.args[0]}") from None
        if path == "/v1/audio":
            tasks = [t for t in q.get("tasks", "").split(",") if t]
            x = codec.read_wav(body)
            with self.busy:
                return 200, self.engine.audio(x, tasks, q.get("language", ""))
        if path == "/v1/frames":
            tasks = [t for t in q.get("tasks", "").split(",") if t]
            try:
                every = float(q.get("ocr_every", "2"))
            except ValueError:
                raise BadRequest("invalid ocr_every") from None
            frames = codec.read_frames(body)
            if len(frames) > 2000:
                raise BadRequest("at most 2000 frames per request")
            with self.busy:
                return 200, self.engine.frames(frames, tasks, every)
        if path == "/v1/text":
            req = _json(body)
            texts = req.get("texts")
            if not isinstance(texts, list) or not texts or len(texts) > 256 or not all(isinstance(t, str) and 0 < len(t) <= 1000 for t in texts):
                raise BadRequest("texts must be 1-256 strings of 1-1000 characters")
            with self.busy:
                return 200, self.engine.text_embeddings(str(req.get("model", "")), texts)
        if path == "/v1/plan":
            req = _json(body)
            desc, guide = req.get("description"), req.get("guide", "")
            if not isinstance(desc, str) or not 0 < len(desc) <= 1000 or not isinstance(guide, str) or len(guide) > 20000:
                raise BadRequest("invalid description")
            if not self.reg.installed("planner"):
                return 501, {"error": "no local language model is installed"}
            with self.busy:
                return 200, self.engine.plan(desc, guide)
        return 404, {"error": "not found"}


def _json(body: bytes) -> dict:
    try:
        v = json.loads(body or b"{}")
    except ValueError:
        raise BadRequest("invalid JSON") from None
    if not isinstance(v, dict):
        raise BadRequest("expected a JSON object")
    return v


def make_handler(app: App):
    class Handler(BaseHTTPRequestHandler):
        server_version = "vaultdock-smart-worker"
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt, *args):  # no request logging (no content, no paths of media)
            pass

        def _send(self, status, value):
            b = json.dumps(value, separators=(",", ":")).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(b)))
            self.end_headers()
            self.wfile.write(b)

        def _do(self, method):
            u = urlparse(self.path)
            if method == "GET" and u.path == "/healthz":  # liveness only, no details
                return self._send(200, {"ok": True})
            if not app.authorised(self.headers.get("Authorization", "")):
                self._drain()
                return self._send(401, {"error": "unauthorised"})
            n = int(self.headers.get("Content-Length") or 0)
            limit = config.MAX_FRAME_BYTES if u.path == "/v1/frames" else config.MAX_AUDIO_BYTES
            if n < 0 or n > limit:
                self.close_connection = True
                return self._send(413, {"error": "request too large"})
            body = self.rfile.read(n) if n else b""
            started = time.monotonic()
            try:
                status, value = app.handle(method, u.path, parse_qs(u.query), body)
            except BadRequest as e:
                status, value = 400, {"error": str(e)}
            except NotInstalled as e:
                status, value = 409, {"error": str(e)}
            except Exception as e:
                log.exception("request failed: %s", u.path)
                status, value = 500, {"error": f"{type(e).__name__}: {e}"[:500]}
            if u.path != "/v1/status":
                log.info("%s %s %d %.1fs", method, u.path, status, time.monotonic() - started)
            self._send(status, value)

        def _drain(self):
            n = int(self.headers.get("Content-Length") or 0)
            if 0 < n <= config.MAX_FRAME_BYTES:
                self.rfile.read(n)
            elif n:
                self.close_connection = True

        def do_GET(self):
            self._do("GET")

        def do_POST(self):
            self._do("POST")

    return Handler


def janitor(app: App, stop: threading.Event):
    while not stop.wait(30):
        if config.IDLE_UNLOAD > 0 and app.busy.n == 0:
            app.reg.unload_idle(config.IDLE_UNLOAD)


def serve(app: App, listen: str = None):
    host, _, port = (listen or config.LISTEN).rpartition(":")
    srv = ThreadingHTTPServer((host or "0.0.0.0", int(port)), make_handler(app))
    srv.daemon_threads = True
    stop = threading.Event()
    threading.Thread(target=janitor, args=(app, stop), daemon=True).start()
    log.info("smart worker %s listening on %s (device %s)", VERSION, listen or config.LISTEN, app.status()["device"])
    try:
        srv.serve_forever()
    finally:
        stop.set()
        srv.server_close()
