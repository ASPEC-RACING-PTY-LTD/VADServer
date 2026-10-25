import logging
import os
import sys

# Nothing is reported anywhere: no telemetry from the libraries.
os.environ.setdefault("HF_HUB_DISABLE_TELEMETRY", "1")
os.environ.setdefault("DO_NOT_TRACK", "1")
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

from . import config  # noqa: E402
from .engine import Engine  # noqa: E402
from .registry import Registry  # noqa: E402
from .server import App, serve  # noqa: E402


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s", stream=sys.stderr)
    if not config.TOKEN and not config.ALLOW_NO_TOKEN:
        logging.error("VAULTDOCK_SMART_TOKEN is not set; set it (the same value as the VaultDock server) or SMART_ALLOW_NO_TOKEN=1")
        sys.exit(2)
    serve(App(Engine(Registry())))


if __name__ == "__main__":
    main()
