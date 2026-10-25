"""API, wire format and registry tests (no models needed)."""

import base64
import json
import threading
import urllib.error
import urllib.request

import numpy as np
import pytest

from vdsmart import codec
from vdsmart.engine import extract_json, parse_tsv, softmax
from vdsmart.registry import MARKER, SPECS, NotInstalled, Registry
from vdsmart.server import App, make_handler


class FakeEngine:
    def __init__(self, reg):
        self.reg = reg
        self.calls = []

    def audio(self, x, tasks, language=""):
        self.calls.append(("audio", len(x), sorted(tasks), language))
        if "events" in tasks:
            raise NotInstalled("ast")
        return {"vad": [{"start": 0, "end": len(x) / codec.SAMPLE_RATE, "score": 1}]}

    def frames(self, frames, tasks, ocr_every):
        self.calls.append(("frames", [t for t, _ in frames], sorted(tasks), ocr_every))
        return {"clip": codec.vecs([t for t, _ in frames], np.ones((len(frames), 4)))}

    def text_embeddings(self, model, texts):
        return {**codec.vecs([0.0] * len(texts), codec.normalize(np.ones((len(texts), 3)))), "scale": 100.0}

    def plan(self, d, g):
        raise AssertionError("not installed")


@pytest.fixture()
def server(tmp_path):
    from http.server import ThreadingHTTPServer

    reg = Registry(str(tmp_path))
    eng = FakeEngine(reg)
    app = App(eng, token="secret", allow_no_token=False)
    srv = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(app))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{srv.server_address[1]}", eng, reg
    srv.shutdown()


def call(base, method, path, body=None, token="secret", ctype="application/json"):
    req = urllib.request.Request(base + path, data=body, method=method)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if body is not None:
        req.add_header("Content-Type", ctype)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read())


def test_auth_required(server):
    base, _, _ = server
    assert call(base, "GET", "/v1/status", token="")[0] == 401
    assert call(base, "GET", "/v1/status", token="wrong")[0] == 401
    assert call(base, "GET", "/healthz", token="")[0] == 200


def test_no_token_refuses_unless_allowed(tmp_path):
    reg = Registry(str(tmp_path))
    assert not App(FakeEngine(reg), token="", allow_no_token=False).authorised("")
    assert App(FakeEngine(reg), token="", allow_no_token=True).authorised("")


def test_status_lists_models(server):
    base, _, _ = server
    st, s = call(base, "GET", "/v1/status")
    assert st == 200
    assert set(s["models"]) == set(SPECS)
    for name, m in s["models"].items():
        assert m["license"] and m["capability"] and m["title"]
        if SPECS[name].kind == "hf":
            assert m["installed"] is False
    assert s["models"]["planner"]["optional"] is True
    assert {"busy", "rss", "cpu_percent", "mem_total", "device"} <= set(s)


def test_audio_roundtrip(server):
    base, eng, _ = server
    wav = codec.wav_bytes(np.zeros(codec.SAMPLE_RATE * 2, dtype=np.float32))
    st, r = call(base, "POST", "/v1/audio?tasks=vad&language=en", wav, ctype="audio/wav")
    assert st == 200 and r["vad"][0]["end"] == 2.0
    assert eng.calls[-1] == ("audio", codec.SAMPLE_RATE * 2, ["vad"], "en")
    # A missing model is a conflict, not a server error.
    st, r = call(base, "POST", "/v1/audio?tasks=events", wav, ctype="audio/wav")
    assert st == 409 and "ast" in r["error"]


def test_audio_rejects_bad_wav(server):
    base, _, _ = server
    assert call(base, "POST", "/v1/audio?tasks=vad", b"not a wav", ctype="audio/wav")[0] == 400
    import io
    import wave

    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(44100)
        w.writeframes(b"\0" * 400)
    assert call(base, "POST", "/v1/audio?tasks=vad", buf.getvalue(), ctype="audio/wav")[0] == 400


def test_frames_roundtrip(server):
    base, eng, _ = server
    body = codec.encode_frames([(1.5, b"\xff\xd8a"), (2.5, b"\xff\xd8b")])
    st, r = call(base, "POST", "/v1/frames?tasks=clip&ocr_every=3", body, ctype="application/octet-stream")
    assert st == 200 and r["clip"]["times"] == [1.5, 2.5] and r["clip"]["dim"] == 4
    assert eng.calls[-1] == ("frames", [1.5, 2.5], ["clip"], 3.0)
    assert call(base, "POST", "/v1/frames?tasks=clip", body[:-1], ctype="application/octet-stream")[0] == 400
    assert call(base, "POST", "/v1/frames?tasks=clip", body[:20], ctype="application/octet-stream")[0] == 400


def test_text_validation(server):
    base, _, _ = server
    assert call(base, "POST", "/v1/text", json.dumps({"model": "clip", "texts": []}).encode())[0] == 400
    assert call(base, "POST", "/v1/text", json.dumps({"model": "clip", "texts": ["x" * 1001]}).encode())[0] == 400
    st, r = call(base, "POST", "/v1/text", json.dumps({"model": "clip", "texts": ["a", "b"]}).encode())
    assert st == 200 and r["dim"] == 3 and r["scale"] == 100.0


def test_plan_without_model_is_501(server):
    base, _, _ = server
    st, _ = call(base, "POST", "/v1/plan", json.dumps({"description": "funny bits", "guide": ""}).encode())
    assert st == 501


def test_models_unknown_and_builtin(server):
    base, _, _ = server
    assert call(base, "POST", "/v1/models/install", json.dumps({"models": ["nope"]}).encode())[0] == 400
    st, r = call(base, "POST", "/v1/models/install", json.dumps({"models": ["tesseract"]}).encode())
    assert st == 202 and r["started"] == []


def test_registry_marker_and_remove(tmp_path):
    reg = Registry(str(tmp_path))
    d = tmp_path / "minilm"
    d.mkdir()
    (d / "config.json").write_text("{}")
    assert not reg.installed("minilm")
    (d / MARKER).write_text("{}")
    assert reg.installed("minilm")
    loads = []
    assert reg.get("minilm", lambda p: loads.append(p) or "model") == "model"
    assert reg.get("minilm", lambda p: loads.append(p) or "other") == "model"
    assert loads == [str(d)]
    assert reg.status()["minilm"]["loaded"]
    reg.unload_idle(-1)
    assert not reg.status()["minilm"]["loaded"]
    reg.remove(["minilm"])
    assert not d.exists() and not reg.installed("minilm")
    with pytest.raises(NotInstalled):
        reg.get("minilm", lambda p: "x")


def test_registry_download_failure_reported(tmp_path, monkeypatch):
    import vdsmart.registry as r

    def boom(spec, dest):
        raise RuntimeError("offline")

    monkeypatch.setattr(r, "_fetch_hf", boom)
    reg = Registry(str(tmp_path))
    assert reg.install(["minilm"]) == ["minilm"]
    reg.downloads["minilm"]["thread"].join(5)
    st = reg.status()["minilm"]
    assert not st["installed"] and "offline" in st["error"]
    assert not (tmp_path / ".partial" / "minilm").exists()


def test_registry_download_success(tmp_path, monkeypatch):
    import vdsmart.registry as r

    def fake(spec, dest):
        with open(f"{dest}/config.json", "w") as f:
            f.write("{}")

    monkeypatch.setattr(r, "_fetch_hf", fake)
    reg = Registry(str(tmp_path))
    reg.install(["minilm"])
    reg.downloads["minilm"]["thread"].join(5)
    assert reg.installed("minilm") and reg.status()["minilm"]["progress"] == 1.0


def test_codec_formats():
    rows = codec.normalize(np.array([[3.0, 4.0], [0.0, 0.0]]))
    assert np.allclose(rows[0], [0.6, 0.8]) and np.allclose(rows[1], [0, 0])
    v = codec.vecs([0.0, 1.0], rows, 2.0)
    raw = np.frombuffer(base64.b64decode(v["f16"]), dtype="<f2")
    assert v["dim"] == 2 and v["window"] == 2.0 and np.allclose(raw[:2], [0.6, 0.8], atol=1e-3)
    m = codec.matrix(1.0, 2.0, ["a", "b"], [[0.0, 1.0], [0.5, 2.0]])
    assert list(base64.b64decode(m["data"])) == [0, 255, 128, 255] and m["n"] == 2
    x = np.sin(np.linspace(0, 100, 16000)).astype(np.float32) * 0.5
    assert np.allclose(codec.read_wav(codec.wav_bytes(x)), x, atol=1e-4)
    fr = [(0.25, b"abc"), (1e6, b"\x00" * 5)]
    assert codec.read_frames(codec.encode_frames(fr)) == fr


def test_extract_json_and_tsv():
    assert extract_json('Sure! {"rules": [{"name": "a {b}"}], "notes": []} done') == {"rules": [{"name": "a {b}"}], "notes": []}
    assert extract_json("no json {broken") is None
    tsv = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" \
          "5\t1\t1\t1\t1\t1\t100\t50\t40\t20\t91.5\tSCORE\n" \
          "5\t1\t1\t1\t1\t2\t0\t0\t10\t10\t12\tjunk\n" \
          "4\t1\t1\t1\t1\t0\t0\t0\t10\t10\t-1\t\n"
    w = parse_tsv(tsv, 200, 100)
    assert w == [{"text": "SCORE", "box": [0.5, 0.5, 0.2, 0.2], "conf": 91.5}]
    p = softmax(np.array([[1.0, 1.0], [10.0, 0.0]]))
    assert np.allclose(p[0], [0.5, 0.5]) and p[1][0] > 0.99
