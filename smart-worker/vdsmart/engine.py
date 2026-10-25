"""Inference: audio, frames, text and the optional filter planner.

Heavy libraries are imported when a model is first used, so the worker
starts quickly and its tests run without them.
"""

import json
import math
import os
import re
import subprocess
from concurrent.futures import ThreadPoolExecutor

import numpy as np

from . import codec, config
from .codec import SAMPLE_RATE, BadRequest
from .registry import NotInstalled, Registry

AUDIO_TASKS = {"vad", "transcript", "events", "clap", "emotion", "speakers", "embed"}
FRAME_TASKS = {"clip", "faces", "ocr"}

# AudioSet rows: one per second covering two seconds (the server aligns
# rows of consecutive chunks by whole seconds).
EVENT_HOP, EVENT_WINDOW = 1.0, 2.0
CLAP_HOP, CLAP_WINDOW = 2.0, 4.0
EMOTION_LABELS = {"neu": "neutral", "hap": "happy", "ang": "angry", "sad": "sad"}
EXPRESSIONS = [
    ("happy", "a photo of a smiling, laughing face"),
    ("surprised", "a photo of a surprised face with an open mouth"),
    ("angry", "a photo of an angry face"),
    ("sad", "a photo of a sad face"),
    ("scared", "a photo of a scared, frightened face"),
    ("neutral", "a photo of a face with a neutral expression"),
]


def _torch():
    import torch

    if config.THREADS > 0 and torch.get_num_threads() != config.THREADS:
        torch.set_num_threads(config.THREADS)
    return torch


def device() -> str:
    if config.DEVICE in ("cpu", "cuda"):
        return config.DEVICE
    try:
        import torch

        return "cuda" if torch.cuda.is_available() else "cpu"
    except Exception:
        return "cpu"


def _rms(x: np.ndarray) -> float:
    return float(np.sqrt(np.mean(np.square(x)))) if x.size else 0.0


class Engine:
    def __init__(self, reg: Registry):
        self.reg = reg

    # --- loaders (each gets the model's directory) ---

    def _whisper(self, path):
        from faster_whisper import WhisperModel

        dev = device()
        return WhisperModel(path, device=dev, compute_type="int8" if dev == "cpu" else "float16", cpu_threads=config.THREADS or 0, num_workers=1)

    def _hf_audio_classifier(self, path, cls_name):
        _torch()
        import transformers

        fe = transformers.AutoFeatureExtractor.from_pretrained(path, local_files_only=True)
        model = getattr(transformers, cls_name).from_pretrained(path, local_files_only=True).eval().to(device())
        return fe, model

    def _clap(self, path):
        _torch()
        from transformers import ClapModel, ClapProcessor

        return ClapProcessor.from_pretrained(path, local_files_only=True), ClapModel.from_pretrained(path, local_files_only=True).eval().to(device())

    def _clip(self, path):
        _torch()
        from transformers import CLIPModel, CLIPProcessor

        return CLIPProcessor.from_pretrained(path, local_files_only=True), CLIPModel.from_pretrained(path, local_files_only=True).eval().to(device())

    def _minilm(self, path):
        _torch()
        from transformers import AutoModel, AutoTokenizer

        return AutoTokenizer.from_pretrained(path, local_files_only=True), AutoModel.from_pretrained(path, local_files_only=True).eval().to(device())

    def _ecapa(self, path):
        _torch()
        from speechbrain.inference.speaker import EncoderClassifier

        return EncoderClassifier.from_hparams(source=path, savedir=os.path.join(path, ".sb"), overrides={"pretrained_path": path}, run_opts={"device": device()})

    def _yunet(self, path):
        import cv2

        files = [f for f in os.listdir(path) if f.endswith(".onnx")]
        if not files:
            raise RuntimeError("the face model file is missing")
        return os.path.join(path, files[0]), cv2

    def _planner(self, path):
        _torch()
        from transformers import AutoModelForCausalLM, AutoTokenizer

        tok = AutoTokenizer.from_pretrained(path, local_files_only=True)
        model = AutoModelForCausalLM.from_pretrained(path, local_files_only=True, torch_dtype="auto").eval().to(device())
        return tok, model

    # --- audio ---

    def audio(self, x: np.ndarray, tasks, language: str = "") -> dict:
        tasks = set(tasks)
        bad = tasks - AUDIO_TASKS
        if bad:
            raise BadRequest("unknown tasks: " + ", ".join(sorted(bad)))
        if language and not re.fullmatch(r"[a-z]{2,3}", language):
            raise BadRequest("invalid language")
        out = {}
        speech = None
        if tasks & {"vad", "emotion"}:
            speech = self.vad(x)
            if "vad" in tasks:
                out["vad"] = speech
        if "transcript" in tasks:
            out["transcript"] = self.transcribe(x, language, "embed" in tasks, "speakers" in tasks)
        if "events" in tasks:
            out["events"] = self.events(x)
        if "clap" in tasks:
            out["clap"] = self.clap_audio(x)
        if "emotion" in tasks:
            out["emotion"] = self.emotion(x, speech)
        return out

    def vad(self, x: np.ndarray):
        if not self.reg.installed("silero-vad"):
            raise NotInstalled("silero-vad")
        from faster_whisper.vad import VadOptions, get_speech_timestamps

        ts = get_speech_timestamps(x, VadOptions(onset=0.5, min_silence_duration_ms=400, speech_pad_ms=150))
        return [{"start": round(t["start"] / SAMPLE_RATE, 3), "end": round(t["end"] / SAMPLE_RATE, 3), "score": 1} for t in ts]

    def transcribe(self, x: np.ndarray, language: str, embed: bool, speakers: bool) -> dict:
        model = self.reg.get("whisper", self._whisper)
        segs, info = model.transcribe(x, language=language or None, beam_size=config.WHISPER_BEAM, word_timestamps=True, vad_filter=True,
                                      condition_on_previous_text=False)
        utt = []
        for s in segs:
            text = s.text.strip()
            if not text:
                continue
            words = [{"w": w.word.strip(), "s": round(w.start, 3), "e": round(w.end, 3), "p": round(w.probability, 3)} for w in (s.words or []) if w.word.strip()]
            utt.append({"start": round(s.start, 3), "end": round(s.end, 3), "text": text, "no_speech": round(s.no_speech_prob, 3), "words": words})
        out = {"language": info.language or language, "model": os.path.basename(config.WHISPER_REPO.rstrip("/")), "utterances": utt}
        if utt and embed and self.reg.installed("minilm"):
            out["embeddings"] = codec.vecs([u["start"] for u in utt], self.sentence_embeddings([u["text"] for u in utt]))
        if utt and speakers and self.reg.installed("ecapa"):
            out["speakers"] = codec.vecs([u["start"] for u in utt], self.speaker_embeddings(x, utt))
        return out

    def sentence_embeddings(self, texts):
        torch = _torch()
        tok, model = self.reg.get("minilm", self._minilm)
        rows = []
        for i in range(0, len(texts), 64):
            t = tok(texts[i : i + 64], padding=True, truncation=True, max_length=256, return_tensors="pt").to(model.device)
            with torch.inference_mode():
                h = model(**t).last_hidden_state
                m = t["attention_mask"].unsqueeze(-1).float()
                rows.append(((h * m).sum(1) / m.sum(1).clamp(min=1e-6)).cpu().numpy())
        return codec.normalize(np.concatenate(rows))

    def speaker_embeddings(self, x: np.ndarray, utt):
        torch = _torch()
        enc = self.reg.get("ecapa", self._ecapa)
        rows = []
        for u in utt:
            mid = (u["start"] + u["end"]) / 2
            half = min(max((u["end"] - u["start"]) / 2, 0.75), 4.0)
            a, b = int(max(mid - half, 0) * SAMPLE_RATE), int(min(mid + half, len(x) / SAMPLE_RATE) * SAMPLE_RATE)
            seg = x[a:b] if b - a >= SAMPLE_RATE // 4 else np.zeros(SAMPLE_RATE, dtype=np.float32)
            with torch.inference_mode():
                e = enc.encode_batch(torch.from_numpy(np.ascontiguousarray(seg))[None]).reshape(-1).cpu().numpy()
            rows.append(e)
        return codec.normalize(np.stack(rows))

    def events(self, x: np.ndarray) -> dict:
        torch = _torch()
        fe, model = self.reg.get("ast", lambda p: self._hf_audio_classifier(p, "ASTForAudioClassification"))
        labels = [model.config.id2label[i] for i in range(model.config.num_labels)]
        dur = len(x) / SAMPLE_RATE
        n = int(math.ceil(dur / EVENT_HOP - 1e-6))
        scores = np.zeros((n, len(labels)), dtype=np.float32)
        todo = []
        for i in range(n):
            a = int(i * EVENT_HOP * SAMPLE_RATE)
            w = x[a : a + int(EVENT_WINDOW * SAMPLE_RATE)]
            if w.size >= SAMPLE_RATE // 4 and _rms(w) > 1e-3:  # silence scores nothing
                todo.append((i, w))
        for k in range(0, len(todo), 8):
            batch = todo[k : k + 8]
            inp = fe([w for _, w in batch], sampling_rate=SAMPLE_RATE, return_tensors="pt").to(model.device)
            with torch.inference_mode():
                p = torch.sigmoid(model(**inp).logits).cpu().numpy()
            for (i, _), row in zip(batch, p):
                scores[i] = row
        return codec.matrix(EVENT_HOP, EVENT_WINDOW, labels, scores)

    def clap_audio(self, x: np.ndarray) -> dict:
        torch = _torch()
        import torchaudio.functional as AF

        proc, model = self.reg.get("clap", self._clap)
        dur = len(x) / SAMPLE_RATE
        times, wins = [], []
        t = 0.0
        while t < max(dur - 0.5, 0.01):
            a = int(t * SAMPLE_RATE)
            wins.append(x[a : a + int(CLAP_WINDOW * SAMPLE_RATE)])
            times.append(t)
            t += CLAP_HOP
        rows = []
        for k in range(0, len(wins), 8):
            batch = [AF.resample(torch.from_numpy(np.ascontiguousarray(w)), SAMPLE_RATE, 48000).numpy() for w in wins[k : k + 8]]
            inp = proc(audios=batch, sampling_rate=48000, return_tensors="pt").to(model.device)
            with torch.inference_mode():
                rows.append(model.get_audio_features(**inp).cpu().numpy())
        emb = codec.normalize(np.concatenate(rows)) if rows else np.zeros((0, 0))
        return codec.vecs(times, emb, CLAP_WINDOW)

    def emotion(self, x: np.ndarray, speech) -> dict:
        torch = _torch()
        fe, model = self.reg.get("ser", lambda p: self._hf_audio_classifier(p, "Wav2Vec2ForSequenceClassification"))
        names = [model.config.id2label[i] for i in range(model.config.num_labels)]
        labels = [EMOTION_LABELS.get(n, n) for n in names]
        spans = []
        for iv in speech or []:
            s = iv["start"]
            while s < iv["end"] - 0.8:
                e = min(s + 6.0, iv["end"])
                spans.append((s, e))
                s = e
        out = []
        for s, e in spans:
            w = x[int(s * SAMPLE_RATE) : int(e * SAMPLE_RATE)]
            inp = fe(w, sampling_rate=SAMPLE_RATE, return_tensors="pt").to(model.device)
            with torch.inference_mode():
                p = torch.softmax(model(**inp).logits[0], -1).cpu().numpy()
            out.append({"start": round(s, 3), "end": round(e, 3), "scores": [round(float(v), 4) for v in p]})
        return {"labels": labels, "spans": out}

    # --- frames ---

    def frames(self, frames, tasks, ocr_every: float = 2.0) -> dict:
        tasks = set(tasks)
        bad = tasks - FRAME_TASKS
        if bad:
            raise BadRequest("unknown tasks: " + ", ".join(sorted(bad)))
        import cv2

        imgs = []
        for t, jpeg in frames:
            img = cv2.imdecode(np.frombuffer(jpeg, dtype=np.uint8), cv2.IMREAD_COLOR)
            if img is None:
                raise BadRequest(f"frame at {t:.2f}s is not a valid JPEG")
            imgs.append((t, img))
        out = {}
        if "clip" in tasks:
            out["clip"] = codec.vecs([t for t, _ in imgs], self.image_embeddings([cv2.cvtColor(i, cv2.COLOR_BGR2RGB) for _, i in imgs]) if imgs else np.zeros((0, 0)))
        if "faces" in tasks:
            out["faces"] = self.faces(imgs)
        if "ocr" in tasks:
            out["ocr"] = self.ocr(imgs, max(ocr_every, 0.0))
        return out

    def image_embeddings(self, rgb):
        torch = _torch()
        proc, model = self.reg.get("clip", self._clip)
        rows = []
        for k in range(0, len(rgb), 32):
            inp = proc(images=rgb[k : k + 32], return_tensors="pt").to(model.device)
            with torch.inference_mode():
                rows.append(model.get_image_features(**inp).cpu().numpy())
        return codec.normalize(np.concatenate(rows))

    def faces(self, imgs) -> dict:
        path, cv2 = self.reg.get("yunet", self._yunet)
        det = cv2.FaceDetectorYN.create(path, "", (320, 320), 0.7, 0.3, 50)
        frames, crops = [], []
        for t, img in imgs:
            h, w = img.shape[:2]
            det.setInputSize((w, h))
            _, found = det.detect(img)
            boxes = []
            if found is not None:
                for f in found:
                    x0, y0, bw, bh = (float(v) for v in f[:4])
                    x0, y0 = max(x0, 0), max(y0, 0)
                    boxes.append([round(x0 / w, 4), round(y0 / h, 4), round(min(bw, w - x0) / w, 4), round(min(bh, h - y0) / h, 4)])
            fr = {"t": round(float(t), 3), "count": len(boxes)}
            if boxes:
                fr["boxes"] = boxes
                big = max(boxes, key=lambda b: b[2] * b[3])
                crops.append((len(frames), _crop(img, big, 0.25)))
            frames.append(fr)
        labels = []
        if crops and self.reg.installed("clip"):
            # Zero-shot expressions of the largest face (CLIP).
            labels = [l for l, _ in EXPRESSIONS]
            tv, scale = self.text_rows("clip", [p for _, p in EXPRESSIONS])
            emb = self.image_embeddings([cv2.cvtColor(c, cv2.COLOR_BGR2RGB) for _, c in crops])
            for (i, _), row in zip(crops, softmax(emb @ tv.T * scale)):
                frames[i]["expr"] = [round(float(v), 4) for v in row]
        return {"expr_labels": labels, "frames": frames}

    def ocr(self, imgs, every: float):
        if not self.reg.installed("tesseract"):
            raise NotInstalled("tesseract")
        import cv2

        picked, last = [], None
        for t, img in imgs:
            if last is None or t - last >= every - 1e-6:
                picked.append((t, img))
                last = t
        lang = os.environ.get("SMART_OCR_LANG", "eng")

        def one(item):
            t, img = item
            h, w = img.shape[:2]
            gray = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
            scale = 2 if w < 1280 else 1
            if scale > 1:
                gray = cv2.resize(gray, None, fx=scale, fy=scale, interpolation=cv2.INTER_CUBIC)
            ok, png = cv2.imencode(".png", gray)
            if not ok:
                return {"t": round(float(t), 3), "words": []}
            env = dict(os.environ, OMP_THREAD_LIMIT="1")
            r = subprocess.run(["tesseract", "stdin", "stdout", "--psm", "11", "-l", lang, "tsv"], input=png.tobytes(), capture_output=True, env=env, timeout=60)
            return {"t": round(float(t), 3), "words": parse_tsv(r.stdout.decode("utf-8", "replace"), w * scale, h * scale)}

        workers = config.THREADS or os.cpu_count() or 1
        with ThreadPoolExecutor(max_workers=max(1, min(workers, 8))) as ex:
            return list(ex.map(one, picked))

    # --- text ---

    def text_rows(self, model: str, texts):
        """Normalised text embeddings and the model's logit scale."""
        torch = _torch()
        if model == "minilm":
            return self.sentence_embeddings(texts), 1.0
        if model == "clip":
            proc, m = self.reg.get("clip", self._clip)
            inp = proc.tokenizer(texts, padding=True, truncation=True, max_length=77, return_tensors="pt").to(m.device)
            with torch.inference_mode():
                emb = m.get_text_features(**inp).cpu().numpy()
            return codec.normalize(emb), float(m.logit_scale.exp().item())
        if model == "clap":
            proc, m = self.reg.get("clap", self._clap)
            inp = proc.tokenizer(texts, padding=True, truncation=True, return_tensors="pt").to(m.device)
            with torch.inference_mode():
                emb = m.get_text_features(**inp).cpu().numpy()
            return codec.normalize(emb), float(m.logit_scale_a.exp().item())
        raise BadRequest("unknown text model")

    def text_embeddings(self, model: str, texts) -> dict:
        rows, scale = self.text_rows(model, texts)
        return {**codec.vecs([0.0] * len(texts), rows), "scale": round(scale, 4)}

    # --- planner ---

    def plan(self, description: str, guide: str) -> dict:
        torch = _torch()
        tok, model = self.reg.get("planner", self._planner)
        msgs = [{"role": "system", "content": PLANNER_PROMPT + "\n\n" + guide}, {"role": "user", "content": description}]
        ids = tok.apply_chat_template(msgs, add_generation_prompt=True, return_tensors="pt").to(model.device)
        with torch.inference_mode():
            out = model.generate(ids, max_new_tokens=700, do_sample=False, pad_token_id=tok.eos_token_id)
        text = tok.decode(out[0][ids.shape[1] :], skip_special_tokens=True)
        f = extract_json(text)
        if f is None:
            raise RuntimeError("the language model did not produce a filter")
        notes = f.pop("notes", []) if isinstance(f.get("notes"), list) else []
        return {"model": os.path.basename(config.PLANNER_REPO.rstrip("/")), "filter": {"rules": f.get("rules", []), "context": f.get("context") or {}},
                "notes": [str(n)[:300] for n in notes][:10]}


PLANNER_PROMPT = """You turn a request to find moments in video recordings into a JSON filter.
Answer with one JSON object only: {"rules": [...], "notes": [...]}.
Each rule: {"name": short name, "category": emotion|conversation|gaming|visual|audio|speech|custom, "when": node}.
A node is a condition {"kind": ..., other fields} or a combination {"op": "and"|"or"|"then"|"without", "window": seconds, "children": [nodes]}.
Use only the condition kinds listed below. If part of the request cannot be detected with them, leave it out and say so in notes.
Never invent kinds, labels or fields."""


def extract_json(text: str):
    """The first balanced JSON object in text."""
    start = text.find("{")
    while start >= 0:
        depth, in_str, esc = 0, False, False
        for i in range(start, len(text)):
            c = text[i]
            if in_str:
                if esc:
                    esc = False
                elif c == "\\":
                    esc = True
                elif c == '"':
                    in_str = False
            elif c == '"':
                in_str = True
            elif c == "{":
                depth += 1
            elif c == "}":
                depth -= 1
                if depth == 0:
                    try:
                        v = json.loads(text[start : i + 1])
                        if isinstance(v, dict):
                            return v
                    except ValueError:
                        pass
                    break
        start = text.find("{", start + 1)
    return None


def parse_tsv(tsv: str, w: float, h: float):
    """Words of Tesseract TSV output with boxes as fractions."""
    words = []
    for line in tsv.splitlines()[1:]:
        p = line.split("\t")
        if len(p) < 12 or p[0] != "5":
            continue
        text = p[11].strip()
        try:
            conf = float(p[10])
            x, y, bw, bh = (float(v) for v in p[6:10])
        except ValueError:
            continue
        if not text or conf < 30:
            continue
        words.append({"text": text, "box": [round(x / w, 4), round(y / h, 4), round(bw / w, 4), round(bh / h, 4)], "conf": round(conf, 1)})
    return words


def softmax(x: np.ndarray) -> np.ndarray:
    x = x - x.max(axis=1, keepdims=True)
    e = np.exp(x)
    return e / e.sum(axis=1, keepdims=True)


def _crop(img, box, pad):
    h, w = img.shape[:2]
    x, y, bw, bh = box[0] * w, box[1] * h, box[2] * w, box[3] * h
    x0, y0 = int(max(x - bw * pad, 0)), int(max(y - bh * pad, 0))
    x1, y1 = int(min(x + bw * (1 + pad), w)), int(min(y + bh * (1 + pad), h))
    if x1 - x0 < 8 or y1 - y0 < 8:
        return img
    return img[y0:y1, x0:x1]
