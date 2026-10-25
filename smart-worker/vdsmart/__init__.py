"""VaultDock smart processing worker.

A local inference service for the VaultDock server: it receives decoded
audio and sampled video frames over HTTP and returns detections and
embeddings. It never sees stored files, sends nothing anywhere and only
downloads models when an administrator installs them.
"""

VERSION = "1.0.0"
