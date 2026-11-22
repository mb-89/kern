"""pyrunner: time.sleep named in a docstring is no use of it."""

import subprocess
import time as t
from datetime import datetime as dt
from kern import component

pyrunner = component("pyrunner")


def run():
    # time.sleep in a comment is no use either
    t.sleep(1)
    when = dt.now()
    out = subprocess.run(["gh", "issue", "list"], capture_output=True)
    return when, out, "ghost"


def elapsed():
    return time.monotonic()
