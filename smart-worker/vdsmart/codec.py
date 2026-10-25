"""Wire formats shared with the server (see internal/smart/worker.go)."""

import base64
import io
import struct
import wave

import numpy as np

SAMPLE_RATE = 16000


class BadRequest(Exception):
    """The request body or parameters are invalid (HTTP 400)."""


def f16(rows) -> str:
    """Encodes embedding rows as base64 little-endian float16."""
    a = np.asarray(rows, dtype=np.float32)
    if a.size == 0:
        return ""
    return base64.b64encode(a.astype("<f2").tobytes()).decode("ascii")


def normalize(a: np.ndarray) -> np.ndarray:
    """L2-normalises rows."""
    a = np.asarray(a, dtype=np.float32)
    if a.ndim == 1:
        a = a[None, :]
    n = np.linalg.norm(a, axis=1, keepdims=True)
    n[n == 0] = 1
    return a / n


def vecs(times, rows, window: float = 0.0) -> dict:
    """An embedding track: times, window, dim and float16 rows."""
    rows = np.asarray(rows, dtype=np.float32)
    dim = int(rows.shape[1]) if rows.ndim == 2 and rows.shape[0] else 0
    out = {"times": [round(float(t), 3) for t in times], "dim": dim, "f16": f16(rows) if dim else ""}
    if window:
        out["window"] = window
    return out


def matrix(hop: float, window: float, labels, scores) -> dict:
    """Per-window class scores (0..1) quantised to bytes."""
    s = np.asarray(scores, dtype=np.float32)
    if s.size == 0:
        return {"hop": hop, "window": window, "labels": list(labels), "data": "", "n": 0}
    q = np.clip(np.rint(np.clip(s, 0, 1) * 255), 0, 255).astype(np.uint8)
    return {"hop": hop, "window": window, "labels": list(labels), "data": base64.b64encode(q.tobytes()).decode("ascii"), "n": int(q.shape[0])}


def read_wav(body: bytes) -> np.ndarray:
    """Reads a 16 kHz mono 16-bit WAV into float32 samples."""
    try:
        with wave.open(io.BytesIO(body)) as w:
            if w.getnchannels() != 1 or w.getsampwidth() != 2 or w.getframerate() != SAMPLE_RATE:
                raise BadRequest("audio must be 16 kHz mono 16-bit WAV")
            raw = w.readframes(w.getnframes())
    except (wave.Error, EOFError) as e:
        raise BadRequest(f"invalid WAV: {e}") from None
    # The server streams a header with the exact length; tolerate an odd
    # trailing byte.
    raw = raw[: len(raw) // 2 * 2]
    return np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0


def read_frames(body: bytes):
    """Reads [float64 time][uint32 length][JPEG] records."""
    out = []
    i = 0
    while i < len(body):
        if i + 12 > len(body):
            raise BadRequest("truncated frame header")
        t, n = struct.unpack_from("<dI", body, i)
        i += 12
        if n == 0 or i + n > len(body):
            raise BadRequest("truncated frame")
        out.append((t, body[i : i + n]))
        i += n
    return out


def encode_frames(frames) -> bytes:
    """The inverse of read_frames (for tests and tools)."""
    b = bytearray()
    for t, jpeg in frames:
        b += struct.pack("<dI", t, len(jpeg))
        b += jpeg
    return bytes(b)


def wav_bytes(samples: np.ndarray) -> bytes:
    """Encodes float samples as 16 kHz mono 16-bit WAV."""
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(SAMPLE_RATE)
        w.writeframes((np.clip(samples, -1, 1) * 32767).astype("<i2").tobytes())
    return buf.getvalue()
