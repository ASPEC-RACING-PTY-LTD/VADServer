"""Settings from the environment."""

import os


def _int(name, default):
    try:
        return int(os.environ.get(name, default))
    except ValueError:
        return default


def _float(name, default):
    try:
        return float(os.environ.get(name, default))
    except ValueError:
        return default


LISTEN = os.environ.get("SMART_LISTEN", "0.0.0.0:8790")
TOKEN = os.environ.get("VAULTDOCK_SMART_TOKEN", "")
# Without a token every request is refused, unless explicitly allowed (a
# worker reachable only on a private Docker network).
ALLOW_NO_TOKEN = os.environ.get("SMART_ALLOW_NO_TOKEN", "") == "1"
MODELS_DIR = os.environ.get("SMART_MODELS_DIR", "/models")
# CPU threads for inference (0: all cores).
THREADS = _int("SMART_THREADS", 0)
# Requests analysed at the same time (each may use THREADS threads).
MAX_JOBS = max(1, _int("SMART_MAX_JOBS", 1))
# Models unused for this long are unloaded (seconds; 0 keeps them).
IDLE_UNLOAD = _float("SMART_IDLE_UNLOAD", 600)
# cpu, cuda or auto.
DEVICE = os.environ.get("SMART_DEVICE", "auto")
WHISPER_REPO = os.environ.get("SMART_WHISPER_REPO", "Systran/faster-whisper-small")
WHISPER_BEAM = max(1, _int("SMART_WHISPER_BEAM", 2))
PLANNER_REPO = os.environ.get("SMART_PLANNER_REPO", "Qwen/Qwen2.5-1.5B-Instruct")
YUNET_URL = os.environ.get(
    "SMART_YUNET_URL",
    "https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx",
)
MAX_AUDIO_BYTES = _int("SMART_MAX_AUDIO_BYTES", 64 << 20)
MAX_FRAME_BYTES = _int("SMART_MAX_FRAME_BYTES", 256 << 20)


def revision(name: str) -> str:
    """A pinned revision of a model repository (SMART_REV_<NAME>)."""
    return os.environ.get("SMART_REV_" + name.upper().replace("-", "_"), "main")
