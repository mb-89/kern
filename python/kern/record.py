"""A hot test's recordings beside an expectation file, read as kern's Go fake reads them (``kerntest/record.go``).

An I/O module's hot test meets the expectations pooled under the names it claims against the real service, and
records each real answer beside the expectation it met: ``<file>.recorded.json`` beside ``<file>.toml``. A
reader's fake answers from the recording where one exists, says where one is older than a set age, and refuses
one that no longer fits its expectation (README.md "Hot tests")::

    {"recordings": [
      {"expect": 1, "name": "github.issue", "request": "issue", "ask": {...}, "hot": "github",
       "at": "2026-11-22T09:00:00Z", "answer": {...}}
    ]}

``kerntest/testdata/record-cases.json`` holds both readers to the same refusals and stale lines.
"""

from __future__ import annotations

import datetime
import json
from pathlib import Path
from typing import Any

from . import expect

#: The age past which a reader's fake says a recording is stale: the hot test has not run again since.
RECORDING_AGE = datetime.timedelta(days=30)

_KEYS = {"expect", "name", "port", "request", "given", "ask", "hot", "at", "value", "answer", "refused"}


def recordings_file(path: str | Path) -> str:
    """Where the recordings of the expectation file path stand: beside it, .toml replaced by .recorded.json."""
    s = str(path)
    return (s[: -len(".toml")] if s.endswith(".toml") else s) + ".recorded.json"


def _recorded(path: str) -> list[dict[str, Any]]:
    file = Path(recordings_file(path))
    if not file.exists():
        return []
    doc = json.loads(file.read_text(encoding="utf-8"))
    if not isinstance(doc, dict) or set(doc) - {"recordings"}:
        raise expect.ExpectationError(f"{file}: a recordings file holds recordings alone")
    recs = doc.get("recordings") or []
    for r in recs:
        unknown = set(r) - _KEYS
        if unknown:
            raise expect.ExpectationError(f"{file}: unknown field {sorted(unknown)[0]!r}")
    return recs


def _at(r: dict[str, Any]) -> datetime.datetime | None:
    text = r.get("at") or ""
    if not text:
        return None
    return datetime.datetime.fromisoformat(text.replace("Z", "+00:00"))


def _fits(r: dict[str, Any], e: expect.Expectation) -> None:
    """Raise why r is no recording of e."""
    if not r.get("hot"):
        raise expect.ExpectationError("it names no hot test")
    if _at(r) is None:
        raise expect.ExpectationError("it names no time")
    if r.get("name", "") != e.name:
        raise expect.ExpectationError(f"it is of name {json.dumps(r.get('name', ''))}, and the expectation of "
                                      f"{json.dumps(e.name)}")
    if r.get("port", "") != e.port or r.get("request", "") != e.request:
        raise expect.ExpectationError("it is of another port or request")
    if r.get("given", "") != e.given:
        raise expect.ExpectationError(f"it is given {json.dumps(r.get('given', ''))}, and the expectation "
                                      f"{json.dumps(e.given)}")
    if bool(r.get("refused", False)) != e.refused:
        raise expect.ExpectationError("it is refused where the expectation is answered, or answered where it is refused")
    if e.request and expect.show(r.get("ask")) != expect.show(e.ask):
        raise expect.ExpectationError(f"it asks {expect.show(r.get('ask'))}, and the expectation {expect.show(e.ask)}")
    if e.port:
        expect.match("value", e.value, r.get("value"), e.exact)
    elif not e.refused:
        expect.match("answer", e.answer, r.get("answer"), e.exact)


def read(x: expect.Expectations) -> dict[int, dict[str, Any]]:
    """The recordings beside x's file, by the place of the expectation each is of, as ``ReadRecordings``: raise
    naming each one that no longer fits its expectation, and the hot test to run again."""
    if not x.file:
        return {}
    recs = _recorded(x.file)
    file = recordings_file(x.file)
    by_n = {e.n: e for e in x.expect}
    out: dict[int, dict[str, Any]] = {}
    errs: list[str] = []
    for r in recs:
        n, hot = r.get("expect"), r.get("hot", "")
        if n in out:
            errs.append(f"expect {n} is recorded twice")
            continue
        e = by_n.get(n)  # type: ignore[arg-type]
        if e is None:
            errs.append(f"the recording of expect {n} by hot test {hot} is of an expectation {x.file} no longer "
                        f"holds: run hot test {hot} again")
            continue
        try:
            _fits(r, e)
        except expect.ExpectationError as err:
            errs.append(f"the recording of {e} by hot test {hot} no longer fits it: {err}: run hot test {hot} again")
            continue
        out[n] = r  # type: ignore[index]
    if errs:
        raise expect.ExpectationError(f"misfit: {file}: " + "; ".join(errs))
    return out


def _days(d: datetime.timedelta) -> str:
    if d < datetime.timedelta(days=1):
        return "under a day"
    return f"{d // datetime.timedelta(days=1)} days"


def stale(x: expect.Expectations, recs: dict[int, dict[str, Any]], now: datetime.datetime,
          max_age: datetime.timedelta = RECORDING_AGE) -> list[str]:
    """A line for each recording older than max_age at now, as ``StaleRecordings`` says it."""
    lines = []
    for e in x.expect:
        r = recs.get(e.n)
        if r is None:
            continue
        at = _at(r)
        assert at is not None
        age = now - at
        if age > max_age:
            hot = r["hot"]
            when = at.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
            lines.append(f"stale: {e} of {x.file} is answered from hot test {hot}'s recording of {when}, "
                         f"{_days(age)} old, over {_days(max_age)}: run hot test {hot} again")
    return lines
