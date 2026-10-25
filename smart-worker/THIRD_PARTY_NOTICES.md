# Smart worker: third-party notices

The smart processing worker image (`ghcr.io/aspec-racing-pty-ltd/vad-smart-worker`) bundles the software below. Models are **not** in the image: they are downloaded from their publishers only when an administrator installs them, and each is used under its own licence. The worker code itself (`vdsmart/`) is covered by the repository's PolyForm Noncommercial License 1.0.0.

## Bundled software

| Component | Version | Licence |
|---|---|---|
| Python | 3.12 (Debian bookworm image) | PSF-2.0 |
| PyTorch, torchaudio | 2.5.1 | BSD-3-Clause |
| numpy | 1.26.4 | BSD-3-Clause |
| scipy | 1.14.1 | BSD-3-Clause |
| Hugging Face transformers | 4.46.3 | Apache-2.0 |
| tokenizers | 0.20.3 | Apache-2.0 |
| huggingface_hub | 0.26.5 | Apache-2.0 |
| faster-whisper (bundles the Silero VAD ONNX model, MIT) | 1.1.0 | MIT |
| CTranslate2 | 4.x (faster-whisper dependency) | MIT |
| ONNX Runtime | 1.x (faster-whisper dependency) | MIT |
| PyAV (bundles FFmpeg libraries, LGPL-2.1+) | faster-whisper dependency | BSD-3-Clause |
| OpenCV (opencv-python-headless) | 4.10.0.84 | Apache-2.0 |
| Pillow | 11.0.0 | MIT-CMU (HPND) |
| SpeechBrain | 1.0.2 | Apache-2.0 |
| Tesseract OCR + English data (Debian packages) | bookworm | Apache-2.0 |
| Leptonica (Tesseract dependency) | bookworm | BSD-2-Clause style |

Transitive Python dependencies are under permissive licences (MIT, BSD, Apache-2.0, PSF).

## Models (installed on demand)

| Name | Model | Licence | Source |
|---|---|---|---|
| silero-vad | Silero VAD v5 | MIT | bundled with faster-whisper |
| whisper | faster-whisper small (Whisper by OpenAI) | MIT | huggingface.co/Systran/faster-whisper-small |
| ast | Audio Spectrogram Transformer, AudioSet | BSD-3-Clause | huggingface.co/MIT/ast-finetuned-audioset-10-10-0.4593 |
| clap | CLAP HTSAT unfused (LAION) | Apache-2.0 | huggingface.co/laion/clap-htsat-unfused |
| ser | wav2vec2 base, SUPERB emotion recognition | Apache-2.0 | huggingface.co/superb/wav2vec2-base-superb-er |
| ecapa | ECAPA-TDNN VoxCeleb (SpeechBrain), optional | Apache-2.0 | huggingface.co/speechbrain/spkrec-ecapa-voxceleb |
| clip | CLIP ViT-B/32 (OpenAI) | MIT | huggingface.co/openai/clip-vit-base-patch32 |
| yunet | YuNet face detector (OpenCV Zoo) | MIT | github.com/opencv/opencv_zoo |
| minilm | all-MiniLM-L6-v2 | Apache-2.0 | huggingface.co/sentence-transformers/all-MiniLM-L6-v2 |
| planner | Qwen2.5-1.5B-Instruct, optional | Apache-2.0 | huggingface.co/Qwen/Qwen2.5-1.5B-Instruct |

Larger Qwen2.5 3B models are deliberately not offered: they are published under a research licence. Model weights may have been trained on data with its own terms; see each model card.

Test images only (not shipped): espeak-ng (GPL-3.0) generates synthetic speech fixtures.
