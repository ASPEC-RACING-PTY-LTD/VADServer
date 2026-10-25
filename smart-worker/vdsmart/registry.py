"""Model registry: what can be installed, explicit downloads, lazy loading.

Models are only downloaded when an administrator installs them (POST
/v1/models/install). Inference loads them from the local model directory
and never contacts the network. Idle models are unloaded after a while so
the worker's memory use falls back when nothing is processed.
"""

import json
import os
import shutil
import threading
import time
import urllib.request
from dataclasses import dataclass, field

from . import config


@dataclass
class Spec:
    name: str
    title: str
    capability: str
    license: str
    # source: a Hugging Face repository ("hf"), a URL ("url"), part of
    # the image ("builtin") or a program on the PATH ("binary").
    kind: str
    source: str
    estimate: int
    optional: bool = False
    # files to fetch from a repository (patterns); weights are chosen
    # automatically (safetensors preferred).
    files: list = field(default_factory=list)
    weights: bool = True


SPECS = {
    s.name: s
    for s in [
        Spec("silero-vad", "Silero VAD", "Voice activity (when someone speaks)", "MIT", "builtin", "faster-whisper (bundled Silero VAD v5 ONNX)", 2_300_000),
        Spec("whisper", "Whisper (faster-whisper)", "Speech to text with word timestamps", "MIT", "hf", config.WHISPER_REPO, 490_000_000,
             files=["config.json", "model.bin", "tokenizer.json", "vocabulary.*", "preprocessor_config.json"], weights=False),
        Spec("ast", "Audio Spectrogram Transformer (AudioSet)", "527 sound event classes: laughter, screaming, cheering, music, ...", "BSD-3-Clause",
             "hf", "MIT/ast-finetuned-audioset-10-10-0.4593", 350_000_000, files=["config.json", "preprocessor_config.json"]),
        Spec("clap", "CLAP (LAION, HTSAT unfused)", "Sounds matched against free-text descriptions", "Apache-2.0", "hf", "laion/clap-htsat-unfused", 620_000_000,
             files=["config.json", "preprocessor_config.json", "tokenizer*", "vocab.json", "merges.txt", "special_tokens_map.json"]),
        Spec("ser", "wav2vec2 speech emotion (SUPERB ER)", "Voice emotion: angry, happy, sad, neutral", "Apache-2.0", "hf", "superb/wav2vec2-base-superb-er", 380_000_000,
             files=["config.json", "preprocessor_config.json"]),
        Spec("ecapa", "ECAPA-TDNN speaker embeddings (SpeechBrain)", "Anonymous speaker numbers in transcripts", "Apache-2.0", "hf", "speechbrain/spkrec-ecapa-voxceleb",
             90_000_000, optional=True, files=["hyperparams.yaml", "embedding_model.ckpt", "mean_var_norm_emb.ckpt", "label_encoder.txt", "classifier.ckpt"], weights=False),
        Spec("clip", "CLIP ViT-B/32 (OpenAI)", "Frames matched against descriptions; facial expressions", "MIT", "hf", "openai/clip-vit-base-patch32", 610_000_000,
             files=["config.json", "preprocessor_config.json", "tokenizer*", "vocab.json", "merges.txt", "special_tokens_map.json"]),
        Spec("yunet", "YuNet face detector (OpenCV Zoo)", "Faces: counts, people appearing or leaving", "MIT", "url", config.YUNET_URL, 240_000),
        Spec("tesseract", "Tesseract OCR", "On-screen text, scoreboards, kill feeds", "Apache-2.0", "binary", "tesseract (Debian package)", 0),
        Spec("minilm", "all-MiniLM-L6-v2 (sentence-transformers)", "Sentence meaning for dialogue search and topic changes", "Apache-2.0", "hf",
             "sentence-transformers/all-MiniLM-L6-v2", 92_000_000, files=["config.json", "tokenizer*", "vocab.txt", "special_tokens_map.json"]),
        Spec("planner", "Qwen2.5 1.5B Instruct", "Optional: interprets natural-language filters", "Apache-2.0", "hf", config.PLANNER_REPO, 3_100_000_000, optional=True,
             files=["config.json", "generation_config.json", "tokenizer*", "vocab.json", "merges.txt", "model.safetensors.index.json"]),
    ]
}

MARKER = ".vaultdock-model.json"


class Registry:
    def __init__(self, root: str = None):
        self.root = root or config.MODELS_DIR
        self.lock = threading.Lock()
        self.downloads = {}  # name -> {"progress", "error", "thread", "partial"}
        self.errors = {}  # name -> last load error
        self.loaded = {}  # name -> (object, last used)
        self.load_locks = {n: threading.Lock() for n in SPECS}
        os.makedirs(self.root, exist_ok=True)

    # --- installation state ---

    def path(self, name: str) -> str:
        return os.path.join(self.root, name)

    def installed(self, name: str) -> bool:
        s = SPECS[name]
        if s.kind == "builtin":
            return _builtin_available(name)
        if s.kind == "binary":
            return shutil.which(s.source.split()[0]) is not None
        return os.path.exists(os.path.join(self.path(name), MARKER))

    def size(self, name: str) -> int:
        return _dir_size(self.path(name)) if SPECS[name].kind in ("hf", "url") else 0

    def status(self) -> dict:
        out = {}
        with self.lock:
            downloads = dict(self.downloads)
            loaded = set(self.loaded)
        for name, s in SPECS.items():
            d = downloads.get(name)
            m = {
                "title": s.title,
                "capability": s.capability,
                "license": s.license,
                "source": s.source,
                "installed": self.installed(name),
                "loaded": name in loaded,
                "bytes": self.size(name),
                "estimate": s.estimate,
                "downloading": bool(d and d.get("thread") and d["thread"].is_alive()),
                "progress": 0.0,
            }
            if s.optional:
                m["optional"] = True
            if d:
                m["progress"] = round(d.get("progress", 0.0), 3)
                if d.get("error"):
                    m["error"] = d["error"]
            if name in self.errors and not m.get("error"):
                m["error"] = self.errors[name]
            out[name] = m
        return out

    def install(self, names):
        for n in names:
            if n not in SPECS:
                raise KeyError(n)
        started = []
        for n in names:
            s = SPECS[n]
            if s.kind in ("builtin", "binary") or self.installed(n):
                continue
            with self.lock:
                d = self.downloads.get(n)
                if d and d["thread"].is_alive():
                    continue
                d = {"progress": 0.0, "error": "", "partial": os.path.join(self.root, ".partial", n)}
                t = threading.Thread(target=self._download, args=(s, d), daemon=True, name=f"download-{n}")
                d["thread"] = t
                self.downloads[n] = d
            t.start()
            started.append(n)
        return started

    def remove(self, names):
        for n in names:
            if n not in SPECS:
                raise KeyError(n)
        for n in names:
            if SPECS[n].kind not in ("hf", "url"):
                continue
            self.unload(n)
            shutil.rmtree(self.path(n), ignore_errors=True)
            with self.lock:
                self.downloads.pop(n, None)
            self.errors.pop(n, None)

    def _download(self, s: Spec, d: dict):
        partial = d["partial"]
        shutil.rmtree(partial, ignore_errors=True)
        os.makedirs(partial, exist_ok=True)
        stop = threading.Event()

        def watch():
            while not stop.wait(1.0):
                if s.estimate:
                    d["progress"] = min(_dir_size(partial) / s.estimate, 0.99)

        threading.Thread(target=watch, daemon=True).start()
        try:
            if s.kind == "hf":
                _fetch_hf(s, partial)
            else:
                _fetch_url(s.source, os.path.join(partial, os.path.basename(s.source.split("?")[0])))
            info = {"name": s.name, "source": s.source, "installed_at": int(time.time()), "bytes": _dir_size(partial)}
            with open(os.path.join(partial, MARKER), "w") as f:
                json.dump(info, f)
            final = self.path(s.name)
            shutil.rmtree(final, ignore_errors=True)
            os.replace(partial, final)
            d["progress"] = 1.0
            self.errors.pop(s.name, None)
        except Exception as e:  # reported in the status
            d["error"] = f"download failed: {e}"
            shutil.rmtree(partial, ignore_errors=True)
        finally:
            stop.set()

    # --- loading ---

    def get(self, name: str, loader):
        """Returns the loaded model, loading it on first use."""
        with self.lock:
            hit = self.loaded.get(name)
            if hit:
                self.loaded[name] = (hit[0], time.monotonic())
                return hit[0]
        if not self.installed(name):
            raise NotInstalled(name)
        with self.load_locks[name]:
            with self.lock:
                hit = self.loaded.get(name)
                if hit:
                    return hit[0]
            try:
                obj = loader(self.path(name))
            except Exception as e:
                self.errors[name] = f"could not be loaded: {e}"
                raise
            self.errors.pop(name, None)
            with self.lock:
                self.loaded[name] = (obj, time.monotonic())
            return obj

    def unload(self, name: str):
        with self.lock:
            self.loaded.pop(name, None)

    def unload_idle(self, idle: float):
        now = time.monotonic()
        with self.lock:
            drop = [n for n, (_, t) in self.loaded.items() if now - t > idle]
            for n in drop:
                del self.loaded[n]
        if drop:
            _free_memory()


class NotInstalled(Exception):
    def __init__(self, name):
        super().__init__(f"the {name} model is not installed")
        self.name = name


def _builtin_available(name: str) -> bool:
    if name == "silero-vad":
        try:
            import faster_whisper.vad  # noqa: F401

            return True
        except Exception:
            return False
    return False


def _dir_size(p: str) -> int:
    total = 0
    for root, _, files in os.walk(p):
        for f in files:
            try:
                total += os.path.getsize(os.path.join(root, f))
            except OSError:
                pass
    return total


def _fetch_hf(s: Spec, dest: str):
    from huggingface_hub import HfApi, snapshot_download

    patterns = list(s.files)
    if s.weights:
        files = HfApi().list_repo_files(s.source, revision=config.revision(s.name))
        safes = [f for f in files if f.endswith(".safetensors") and "/" not in f]
        if safes:
            patterns += safes
        elif "pytorch_model.bin" in files:
            patterns.append("pytorch_model.bin")
        else:
            raise RuntimeError("the repository has no usable weights")
    snapshot_download(s.source, revision=config.revision(s.name), local_dir=dest, allow_patterns=patterns)
    # Drop the hub's download metadata: the directory only holds the model.
    shutil.rmtree(os.path.join(dest, ".cache"), ignore_errors=True)


def _fetch_url(url: str, dest: str):
    req = urllib.request.Request(url, headers={"User-Agent": "vaultdock-smart-worker"})
    with urllib.request.urlopen(req, timeout=60) as r, open(dest, "wb") as f:
        shutil.copyfileobj(r, f)


def _free_memory():
    import gc

    gc.collect()
    try:
        import torch

        if torch.cuda.is_available():
            torch.cuda.empty_cache()
    except Exception:
        pass
