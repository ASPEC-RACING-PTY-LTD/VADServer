# Smart video processing

Smart processing analyses recordings stored in VaultDock (video chat, gaming, reactions, tutorials, anything), suggests the interesting segments, lets you review them, renders the clips you keep and helps you remove originals you no longer need. It runs entirely on your server: no cloud services, no external AI APIs, no telemetry. The desktop client is not involved.

## Using it

**Smart Processing** in the web app has these tabs: Overview, New Queue, Active Queues, Review, Cleanup, Completed Queues, Detection Presets and (administrators) Settings.

1. **Create a queue.** Choose folders (optionally with subfolders) or select files and folders in **Files → Smart processing → New queue**. Pick presets, add custom conditions (or describe what to find in words), tune the segment settings and where clips go.
2. **Processing runs in the background.** It continues with the browser closed and resumes after a restart or upgrade. Analysis results are cached per file content, so changing a queue's detection re-evaluates recordings without measuring them again; a changed file is measured again.
3. **Review.** Each recording opens in the review editor: a player, a timeline (loudness, motion, speech, every detected moment) and the suggested segments with their reasons and confidence. Approve, reject, drag edges, split, merge, add your own segments, filter by category, read and search the transcript. Every change is saved immediately. Press **?** for keyboard shortcuts (Space, J/K, A/R, I/O, S, M, N, P, Enter, L).
4. **Rendering.** Approved segments are rendered when you finish a recording (**Done**). By default a stream copy is used when the cut lands on a keyframe (fast, lossless) and a frame-accurate high-quality re-encode otherwise. Clips are stored beside the original (or in a subfolder, or one destination folder mirroring the originals' folders), keep the original's dates, never overwrite anything (names are made unique), are verified by probing before they count, and are linked to their source. Rendering the same segment again never creates a second clip. Nothing is rendered without review unless **auto-approve** is on for the queue.
5. **Cleanup.** Originals become eligible once every approved segment has a verified clip. Originals reviewed with nothing kept are listed separately. Select per file, in bulk, for a folder (with subfolders) or a queue, see the recoverable space, and confirm a normal dialog showing the count and size: delete permanently or move to the trash (restorable for 7 days). Right before removal each original is checked again: unchanged, clips present with their recorded content and playable, and not itself a clip of another kept recording. Cleanup never runs automatically and only touches the chosen files of the signed-in user. The desktop app will not upload removed originals again.
6. **Statistics.** Each queue keeps lifetime totals: clips made and their size, originals removed, storage actually freed (confirmed) and still recoverable (estimated), net savings, processing time, failures. Completed and archived queues stay available.

Queue actions: pause, resume, cancel, retry failed, archive, reopen, duplicate, delete (records only; files stay).

## What it can detect

Each condition lists the detector it needs; conditions whose model is not installed are reported and find nothing; they are never silently replaced with something else.

| Category | Conditions | Detector |
|---|---|---|
| Audio | loud, quiet, silence, sudden rise or drop in loudness | Server (FFmpeg), always available |
| Audio | sound events: laughter, screaming, shouting, cheering, applause, crying, gasps, music, gunshots, explosions, alarms, … (527 AudioSet classes) | AST |
| Audio | sounds matching a description | CLAP |
| Audio | music starting or stopping | AST |
| Speech | speaking, speech starts or ends, awkward silences, overlapping speech, speaker changes | Silero VAD, Whisper (+ ECAPA for speaker changes); loudness-based estimate without models |
| Speech | keywords and phrases (any or all within a window), profanity, repeated phrases | Whisper |
| Conversation | questions, dialogue matching a topic or meaning, topic shifts | Whisper + MiniLM |
| Emotion | voice emotion (angry, happy, sad, neutral) | wav2vec2 SER |
| Emotion | facial expressions (happy, surprised, angry, sad, scared, neutral) | YuNet + CLIP |
| Visual | scenes matching a description | CLIP |
| Visual | faces: count, someone appears or leaves | YuNet |
| Visual | motion, sudden movement, scene changes | Server (FFmpeg) |
| Visual | on-screen text | Tesseract |
| Gaming | score changes (scoreboard region), kill feed kills and deaths for a player name | Tesseract |

Conditions combine with **all of** (together within a window), **any of**, **in sequence** (each within a window of the previous) and **without** (first one with none of the others nearby), nested up to six levels. Presets save a combination; the built-in ones (Funny Moments, OmeTV Highlights, Gaming Highlights, Rocket League goals, Conversation Highlights, Loud Reactions, Laughter Detection, Interesting Dialogue, Custom) are all real detector combinations.

Segment settings: confidence threshold, shortest moment, longest segment, footage before and after, merging of nearby moments, trimming of silent ends, not cutting mid-sentence, duplicate overlap and suggestions per recording.

### Describing what to find

Descriptions are interpreted on the server by built-in rules (or, when enabled, the optional local language model). The result lists every condition it uses, what each needs, whether it can run here, and what cannot be detected reliably, for example:

- "me" versus "the other person": speakers are numbered anonymously, not identified;
- counting how many people laugh;
- abstract ideas such as "clutch": the closest signals (excited reactions, sudden action) are used and flagged.

When nothing specific matches, the description is matched by meaning against sounds, frames and speech, and marked approximate.

## Architecture

- **Server (Go):** queues, scheduling, caching, evaluation, segments, rendering, cleanup and statistics, all in the existing SQLite database (`smart_*` tables, migration 15). The background runner claims work from the database, so it survives restarts; interrupted analysis resumes from checkpoints (audio is analysed in chunks, frames in two-minute spans), interrupted renders restart without duplicating clips (each clip is linked to its output in the same transaction that stores it). Temporary decoded audio lives in `state/tmp/smart` (private, removed after each recording and at start); measurements are cached in `state/cache/smart/<user>/<sha>` (0640, removed when the content is no longer stored).
- **Server measurements (no models):** FFmpeg decodes 16 kHz mono audio and 64×36 grey frames; loudness, onsets, speech-band energy, motion, scene changes and brightness are measured in Go.
- **Inference worker (optional, Python):** a separate container (`smart-worker/`) running the models. The server sends it decoded audio chunks and sampled JPEG frames over HTTP with a shared token; it never sees files or paths, logs no content, has no published ports and stores only models.

Without the worker, smart processing still works with the server measurements (loudness, motion, scenes, an energy-based speech estimate).

## Deployment

Enable the worker on the server:

```bash
./install.sh integrations smart on
```

This generates `VAULTDOCK_SMART_TOKEN` in `.env`, creates `state/smart-models`, and adds `deploy/integrations/smart/compose.smart.yml` (image `ghcr.io/aspec-racing-pty-ltd/vad-smart-worker`, about 2.5 GB). Then install models under **Smart Processing → Settings** (or `docker exec vaultdock-smart-worker python -m vdsmart.install whisper ast …`). Nothing is downloaded before that; inference then runs offline. Disable with `./install.sh integrations smart off` (models stay on disk until you delete the folder).

| Model | Download | Memory when loaded |
|---|---|---|
| whisper (small) | 0.5 GB | ~1 GB |
| ast | 0.35 GB | ~0.5 GB |
| clap | 0.6 GB | ~0.8 GB |
| ser | 0.4 GB | ~0.5 GB |
| clip | 0.6 GB | ~0.7 GB |
| minilm | 0.1 GB | ~0.2 GB |
| ecapa (optional) | 0.1 GB | ~0.2 GB |
| yunet, silero-vad, tesseract | < 3 MB each | small |
| planner (optional) | 3.1 GB | ~6 GB |

Models are unloaded after 10 idle minutes (`SMART_IDLE_UNLOAD`). Worker limits: `VAULTDOCK_SMART_CPUS` (default 2) and `VAULTDOCK_SMART_MEMORY` (default 6g). Server-side intensity (Settings): recordings analysed and rendered at once, FFmpeg threads, low priority, hardware decoding, frames per second, frame width, OCR interval, audio chunk length, longest recording, speech language, and on/off. A larger Whisper model can be used with `SMART_WHISPER_REPO` (for example `Systran/faster-whisper-medium`, also MIT).

**GPU (optional, NVIDIA):** build the worker with `--build-arg TORCH_INDEX=https://download.pytorch.org/whl/cu124` and add a GPU reservation in `docker-compose.override.yml` (`deploy.resources.reservations.devices: [{driver: nvidia, count: 1, capabilities: [gpu]}]`). The worker uses CUDA automatically. FFmpeg decoding on the server can use the hardware decoder setting independently.

### Performance (estimates: CPU, 2 cores, standard models)

Per minute of recording, roughly: server measurements 1–2 s; voice activity 1 s; transcription (small, int8) 10–20 s; sound events 10 s; CLAP 3 s; voice emotion 2 s on speech only; frames at 1 fps with CLIP 5 s, faces 1 s, OCR every 2 s 5–10 s. Only the detectors a queue's conditions need are run. Stream-copied clips render in about a second; re-encoded clips at a few times real time. 500 ten-minute clips with transcription and sound events take on the order of a day on 2 cores; a GPU makes the model work 10–20× faster.

## Limitations

- Speaker identity is anonymous (Speaker 1, 2, …): "me" cannot be told apart from others, and the number of people reacting is not counted.
- Emotions and expressions are estimates from voice and faces, shown as uncertain.
- Score and kill-feed reading depend on the on-screen layout (regions are adjustable) and OCR quality; stylised fonts may be missed.
- Meaning-based matching (CLIP, CLAP, MiniLM) is approximate and depends on the wording.
- Whisper can mishear names and slang; keyword matching is only as good as the transcript.
- Queues hold up to 5,000 recordings; longer recordings can be limited by the administrator.

## Security

All processing is local. Requests are scoped to the signed-in user: queues, items, segments and cleanup only ever see that user's files. Folder names in output settings are validated (no traversal). Temporary files are private and cleaned up; logs contain no media content or transcripts. The worker requires a shared token, has no published ports and never receives file paths. No telemetry: Hugging Face telemetry is disabled and models load from local files only.

## Tests

- `internal/smart`: loudness, onset and speech-band measurement, frame motion and scene changes, embedding and score codecs, every condition kind used by the presets, combined conditions (all/any/sequence/without, windows), missing-model warnings, segment context, merging, length limits, sentence snapping, silence trimming, duplicates, description interpretation, filter validation, speaker clustering.
- `internal/store`: queue persistence, claiming, restart recovery, re-evaluation keeping review decisions, failure history, render planning without duplicates, cleanup bookkeeping, per-user scoping.
- `internal/api` (with FFmpeg): an end-to-end run on a synthetic recording with a fake worker: analysis (server loudness + transcript keyword), reasons, transcript search, review edits, rendering beside the original with its dates, re-review without duplicate clips, verified cleanup, statistics, permissions between users, path traversal, queue actions.
- `smart-worker/tests`: API, auth, limits, codecs, model registry and downloads; with `SMART_TEST_MODELS=1` the real models on synthetic speech (espeak-ng), tones, silence, coloured frames and rendered text.
- `web/src/features/smart`: cleanup confirmation (count and size, no typed confirmation), review shortcuts and finishing, description interpretation.
