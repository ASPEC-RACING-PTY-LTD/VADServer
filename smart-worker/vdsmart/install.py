"""Installs models from the command line (explicit setup):

    python -m vdsmart.install whisper ast clip ...
    python -m vdsmart.install --list
"""

import sys
import time

from .registry import SPECS, Registry


def main(argv):
    reg = Registry()
    if not argv or argv[0] == "--list":
        for name, m in reg.status().items():
            state = "installed" if m["installed"] else "not installed"
            print(f"{name:12} {state:14} {m['estimate'] / 1e6:8.0f} MB  {m['license']:14} {m['capability']}")
        return 0
    unknown = [n for n in argv if n not in SPECS]
    if unknown:
        print("unknown models: " + ", ".join(unknown), file=sys.stderr)
        return 2
    reg.install(argv)
    for n, d in list(reg.downloads.items()):
        while d["thread"].is_alive():
            time.sleep(1)
        print(f"{n}: {'failed: ' + d['error'] if d.get('error') else 'installed'}")
    return 1 if any(d.get("error") for d in reg.downloads.values()) else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
