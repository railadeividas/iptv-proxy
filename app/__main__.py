import logging
import os
import signal
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path

from .proxy import Proxy, load_secret
from .server import make_handler
from .sources import SourceManager

LOG = logging.getLogger("iptv")
PORT = 3333


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    secret_path = os.getenv("SIGNING_KEY_FILE", "/data/signing.key")
    try:
        secret = load_secret(secret_path)
        manager = SourceManager(str(Path(secret_path).parent), os.getenv("PLAYLIST_SOURCE", ""))
    except (RuntimeError, ValueError, OSError) as error:
        LOG.error("Cannot start: %s", error)
        raise SystemExit(1)
    proxy = Proxy(secret, manager)
    handler = make_handler(
        proxy, manager,
        password=os.getenv("UI_PASSWORD") or None,
    )
    manager.start()
    server = ThreadingHTTPServer(("0.0.0.0", PORT), handler)
    # As PID 1 in a container, Python ignores SIGTERM unless a handler is installed.
    signal.signal(signal.SIGTERM, lambda *_: threading.Thread(target=server.shutdown).start())
    LOG.info("IPTV proxy and playlist manager listening on port %s", server.server_port)
    server.serve_forever()


if __name__ == "__main__":
    main()
