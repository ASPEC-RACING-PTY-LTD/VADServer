"""Real model tests on synthetic fixtures.

They run when SMART_TEST_MODELS=1 and use models already installed in
SMART_MODELS_DIR (install them first: python -m vdsmart.install NAME...).
Speech fixtures come from espeak-ng when it is available (test images
only; it is not part of the worker image).
"""

import os
import shutil
import subprocess

import numpy as np
import pytest

from vdsmart import codec
from vdsmart.engine import Engine
from vdsmart.registry import Registry

pytestmark = pytest.mark.skipif(os.environ.get("SMART_TEST_MODELS") != "1", reason="set SMART_TEST_MODELS=1 to run model tests")
SR = codec.SAMPLE_RATE


@pytest.fixture(scope="module")
def eng():
    return Engine(Registry())


def need(eng, *names):
    for n in names:
        if not eng.reg.installed(n):
            pytest.skip(f"{n} is not installed")


def tone(seconds, freq=440.0, amp=0.5):
    t = np.arange(int(seconds * SR)) / SR
    return (amp * np.sin(2 * np.pi * freq * t)).astype(np.float32)


def silence(seconds):
    return np.zeros(int(seconds * SR), dtype=np.float32)


def speech(text):
    if not shutil.which("espeak-ng"):
        pytest.skip("espeak-ng is not available for speech fixtures")
    wav = subprocess.run(["espeak-ng", "-v", "en-us", "-s", "150", "--stdout", text], capture_output=True, check=True).stdout
    # espeak-ng writes 22.05 kHz; resample to 16 kHz.
    import io
    import wave

    with wave.open(io.BytesIO(wav)) as w:
        rate, raw = w.getframerate(), w.readframes(w.getnframes())
    x = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768
    n = int(len(x) * SR / rate)
    return np.interp(np.linspace(0, len(x) - 1, n), np.arange(len(x)), x).astype(np.float32)


def test_vad_finds_speech_not_tone(eng):
    need(eng, "silero-vad")
    x = np.concatenate([silence(2), speech("hello there, this is a voice activity test"), silence(3)])
    iv = eng.vad(x)
    assert iv, "speech was not found"
    assert iv[0]["start"] >= 1.5 and iv[-1]["end"] <= len(x) / SR - 2
    assert eng.vad(np.concatenate([silence(1), tone(3)])) == []


def test_transcript_has_words_and_times(eng):
    need(eng, "whisper")
    x = np.concatenate([silence(1.5), speech("good morning everyone, welcome to the test"), silence(1)])
    r = eng.transcribe(x, "en", embed=eng.reg.installed("minilm"), speakers=False)
    text = " ".join(u["text"] for u in r["utterances"]).lower()
    assert "morning" in text and "welcome" in text, text
    words = [w for u in r["utterances"] for w in u["words"]]
    assert words and words[0]["s"] >= 1.0, words[:3]
    assert all(w["e"] >= w["s"] for w in words)
    if "embeddings" in r:
        assert r["embeddings"]["dim"] == 384 and len(r["embeddings"]["times"]) == len(r["utterances"])


def test_audio_events_sine_and_silence(eng):
    need(eng, "ast")
    x = np.concatenate([silence(3), tone(4, 1000)])
    m = eng.events(x)
    assert m["n"] == 7 and m["hop"] == 1.0 and len(m["labels"]) == 527
    import base64

    rows = np.frombuffer(base64.b64decode(m["data"]), dtype=np.uint8).reshape(m["n"], -1)
    assert rows[:2].max() == 0, "silence must score nothing"
    sine = m["labels"].index("Sine wave")
    assert rows[4:, sine].max() > 60, rows[4:, sine]


def test_clap_matches_description(eng):
    need(eng, "clap")
    v = eng.clap_audio(tone(6, 880))
    assert v["dim"] == 512 and v["times"][:2] == [0.0, 2.0]
    rows, scale = eng.text_rows("clap", ["a high pitched electronic beep tone", "a dog barking loudly"])
    emb = np.frombuffer(__import__("base64").b64decode(v["f16"]), dtype="<f2").astype(np.float32).reshape(len(v["times"]), -1)
    s = emb @ rows.T
    assert (s[:, 0] > s[:, 1]).mean() > 0.5, s
    assert scale > 1


def test_emotion_spans(eng):
    need(eng, "ser", "silero-vad")
    x = np.concatenate([silence(1), speech("I am so happy today, this is wonderful news")])
    r = eng.audio(x, ["emotion", "vad"])
    assert r["emotion"]["labels"] and set(r["emotion"]["labels"]) <= {"neutral", "happy", "angry", "sad"}
    assert r["emotion"]["spans"]
    for sp in r["emotion"]["spans"]:
        assert abs(sum(sp["scores"]) - 1) < 0.01


def jpeg(img):
    import cv2

    ok, b = cv2.imencode(".jpg", img)
    assert ok
    return b.tobytes()


def test_clip_frames_match_colours(eng):
    need(eng, "clip")
    red = np.zeros((224, 224, 3), np.uint8)
    red[:, :, 2] = 255
    blue = np.zeros((224, 224, 3), np.uint8)
    blue[:, :, 0] = 255
    r = eng.frames([(0.0, jpeg(red)), (1.0, jpeg(blue))], ["clip"])
    v = r["clip"]
    assert v["dim"] == 512 and v["times"] == [0.0, 1.0]
    emb = np.frombuffer(__import__("base64").b64decode(v["f16"]), dtype="<f2").astype(np.float32).reshape(2, -1)
    t, _ = eng.text_rows("clip", ["a plain red image", "a plain blue image"])
    s = emb @ t.T
    assert s[0, 0] > s[0, 1] and s[1, 1] > s[1, 0], s


def test_ocr_reads_score(eng):
    need(eng, "tesseract")
    import cv2

    img = np.full((360, 640, 3), 30, np.uint8)
    cv2.putText(img, "SCORE 12", (220, 40), cv2.FONT_HERSHEY_SIMPLEX, 1.0, (255, 255, 255), 2)
    cv2.putText(img, "PLAYER1 eliminated BOT", (330, 90), cv2.FONT_HERSHEY_SIMPLEX, 0.6, (255, 255, 255), 2)
    r = eng.frames([(0.0, jpeg(img)), (0.5, jpeg(img)), (2.0, jpeg(img))], ["ocr"], 1.0)
    assert [f["t"] for f in r["ocr"]] == [0.0, 2.0]
    words = {w["text"] for w in r["ocr"][0]["words"]}
    assert "SCORE" in words and "12" in words, words
    sc = next(w for w in r["ocr"][0]["words"] if w["text"] == "12")
    assert 0.4 < sc["box"][0] < 0.7 and sc["box"][1] < 0.15


def test_faces_none_on_blank(eng):
    need(eng, "yunet")
    r = eng.frames([(0.0, jpeg(np.full((360, 640, 3), 128, np.uint8)))], ["faces"])
    assert r["faces"]["frames"] == [{"t": 0.0, "count": 0}]


def test_sentence_embeddings_similarity(eng):
    need(eng, "minilm")
    rows, _ = eng.text_rows("minilm", ["we talked about the football match", "the soccer game last night", "my tax return form"])
    s = rows @ rows.T
    assert s[0, 1] > s[0, 2]


def test_speaker_embeddings_separate_voices(eng):
    need(eng, "ecapa")
    if not shutil.which("espeak-ng"):
        pytest.skip("espeak-ng is not available")
    a = speech("this is the first speaker talking for a while")
    b = (speech("and now a different voice is speaking here") * 0.9)
    x = np.concatenate([a, silence(0.5), b])
    ua = {"start": 0.0, "end": len(a) / SR}
    ub = {"start": len(a) / SR + 0.5, "end": len(x) / SR}
    e = eng.speaker_embeddings(x, [ua, ua, ub])
    assert e.shape == (3, 192)
    assert float(e[0] @ e[1]) > 0.99
