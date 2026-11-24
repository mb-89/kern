"""kern's Python tests run with the network cut, as its Go tests do (``offline/``, README.md "Tests").

Outside a hot test's run (``KERN_HOT``), the session drops the environment's tokens and the
cloud session's mark, points every proxy variable at a port nothing listens on, names loopback
alone as reached without it, and refuses every address lookup that is no loopback one, a name
or a number, so no test and no process it starts reaches the outside.
"""

from __future__ import annotations

import ipaddress
import os
import socket

CLOSED = "http://127.0.0.1:9"
LOOPBACK = "localhost,127.0.0.1,::1"
REFUSAL = "kern's tests run with the network cut (python/tests/conftest.py)"


def _loopback(host: object) -> bool:
    if host is None:
        return True
    name = host.decode() if isinstance(host, bytes) else str(host)
    if name in ("", "localhost"):
        return True
    try:
        return ipaddress.ip_address(name.split("%")[0]).is_loopback
    except ValueError:
        return False


def _cut() -> None:
    for key in ("GH_TOKEN", "GITHUB_TOKEN", "CLAUDE_CODE_REMOTE"):
        os.environ.pop(key, None)
    for key in ("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"):
        os.environ[key] = CLOSED
    os.environ["NO_PROXY"] = os.environ["no_proxy"] = LOOPBACK
    resolve = socket.getaddrinfo

    def getaddrinfo(host, *args, **kwargs):  # type: ignore[no-untyped-def]
        if not _loopback(host):
            raise socket.gaierror(socket.EAI_NONAME, f"{REFUSAL}: {host!r} is no loopback address")
        return resolve(host, *args, **kwargs)

    socket.getaddrinfo = getaddrinfo  # type: ignore[assignment]


if not os.environ.get("KERN_HOT"):
    _cut()
