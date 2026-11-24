"""The Python tests' network cut (``conftest.py``): no address outside loopback resolves."""

from __future__ import annotations

import os
import socket

import pytest


@pytest.mark.skipif(bool(os.environ.get("KERN_HOT")), reason="a hot test's run leaves the network whole")
def test_the_tests_are_cut_off_the_network() -> None:
    for host in ("api.github.com", "140.82.112.6"):
        with pytest.raises(socket.gaierror, match="network cut"):
            socket.getaddrinfo(host, 443)
    assert socket.getaddrinfo("127.0.0.1", 9)
    assert socket.getaddrinfo("localhost", 9)
    assert os.environ["HTTPS_PROXY"] == "http://127.0.0.1:9"
    assert "GH_TOKEN" not in os.environ
