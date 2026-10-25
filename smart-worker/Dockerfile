# VaultDock smart processing worker: local inference for smart video
# processing. Models are not part of the image; an administrator installs
# them from the VaultDock admin page (they are kept in the /models volume).
#
# GPU build (NVIDIA): --build-arg TORCH_INDEX=https://download.pytorch.org/whl/cu124
# and run with the NVIDIA container runtime (see docs/smart-processing.md).
FROM python:3.12-slim-bookworm

ARG TORCH_INDEX=https://download.pytorch.org/whl/cpu

RUN apt-get update \
 && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-eng libgomp1 ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 10001 --create-home --shell /usr/sbin/nologin worker \
 && mkdir /models && chown worker:worker /models

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir --index-url "${TORCH_INDEX}" torch==2.5.1 torchaudio==2.5.1 \
 && pip install --no-cache-dir -r requirements.txt

COPY vdsmart /app/vdsmart

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    HF_HUB_DISABLE_TELEMETRY=1 \
    DO_NOT_TRACK=1 \
    HF_HOME=/models/.hf \
    SMART_MODELS_DIR=/models \
    SMART_LISTEN=0.0.0.0:8790

USER worker
VOLUME /models
EXPOSE 8790
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
  CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8790/healthz', timeout=4)"]
CMD ["python", "-m", "vdsmart"]
