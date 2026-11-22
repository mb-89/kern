"""A component's expectation file, beside its tests, read as kern's Go fake reads it (``kerntest/expect.go``).

It holds what the component's tests expect of each name it reads and each request it sends, and the names
and requests it claims, whose pooled expectations its own tests meet; no line of it names the component on
the other side (README.md "Expectations")::

    [[expect]]                   # a value it reads
    name = "numbers"             # the name it pools under
    port = "numbers"             # its own input port, which the fake seeds
    given = "numbers stand"      # optional: the precondition its claimer sets up
    value = [1, 2, 3]            # the least the reader relies on, matched by type

    [[expect]]                   # a request it sends
    name = "lines.append"
    request = "post"             # its own request port, which the fake answers
    ask = ""
    refused = true               # or answer = <value>
    exact = ["ask"]              # the paths matched by equality; match = "exact" is all of them

    [[claim]]
    name = "numbers"
    port = "numbers"             # the output it writes the name on, or verb = "<verb>"

``kerntest/testdata/expect-cases.json`` holds both readers to the same refusals and matches.
"""

from __future__ import annotations

import datetime
import json
import math
import re
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .declaration import CONFIG_PREFIX, Declaration, fits, kind

_NAME = re.compile(r"^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$")
_EXPECT_KEYS = ("name", "port", "request", "given", "value", "ask", "answer", "refused", "match", "exact")
_CLAIM_KEYS = ("name", "port", "verb")
_ROOTS = ("value", "ask", "answer")


class ExpectationError(ValueError):
    """An expectation file the fake refuses, or a value that does not meet an expectation."""


@dataclass
class Expectation:
    """One thing a reader's tests rely on of a name or a request."""

    name: str
    file: str
    n: int
    port: str = ""
    request: str = ""
    given: str = ""
    value: Any = None
    ask: Any = None
    answer: Any = None
    refused: bool = False
    exact: list[str] = field(default_factory=list)

    def __str__(self) -> str:
        s = f"expect {self.n} ("
        s += f"port {self.port}" if self.port else f"request {self.request}, ask {show(self.ask)}"
        if self.given:
            s += f", given {json.dumps(self.given)}"
        return s + ")"


@dataclass(frozen=True)
class Claim:
    """A name a component writes on an output port, or a request it answers with a verb."""

    name: str
    port: str = ""
    verb: str = ""


@dataclass
class Expectations:
    """One component's expectation file."""

    file: str
    expect: list[Expectation] = field(default_factory=list)
    claims: list[Claim] = field(default_factory=list)

    def check(self, d: Declaration) -> None:
        """Refuse expectations and claims the declaration does not hold, as ``Expectations.Check`` does."""
        errs: list[str] = []
        outputs, verbs = set(d.outputs), set(d.verbs)
        for e in self.expect:
            if e.port:
                t = d.inputs.get(e.port) if not e.port.startswith(CONFIG_PREFIX) else None
                if t is None:
                    errs.append(f'{e}: component {d.name} reads no input "{e.port}"')
                elif not fits(plain(e.value), t):
                    errs.append(f"{e}: value: {show(e.value)} is no {t}")
                continue
            r = d.requests.get(e.request)
            if r is None:
                errs.append(f'{e}: component {d.name} has no request port "{e.request}"')
                continue
            if not fits(plain(e.ask), r.type):
                errs.append(f"{e}: ask: {show(e.ask)} is no {r.type}")
            if e.answer is not None and not fits(plain(e.answer), r.answer):
                errs.append(f"{e}: answer: {show(e.answer)} is no {r.answer}")
        for i, c in enumerate(self.claims, 1):
            if c.port and c.port not in outputs:
                errs.append(f'claim {i}: component {d.name} writes no output "{c.port}"')
            elif c.verb and c.verb not in verbs:
                errs.append(f'claim {i}: component {d.name} accepts no verb "{c.verb}"')
        if errs:
            raise ExpectationError(f"{self.file}: " + "; ".join(errs))


def read(path: str | Path) -> Expectations:
    """Read a component's expectation file, refusing one that is malformed."""
    p = Path(path)
    return parse(str(path), p.read_bytes())


def parse(file: str, data: bytes) -> Expectations:
    """Read an expectation file's text; file names it in every refusal."""
    try:
        doc = tomllib.loads(data.decode())
    except tomllib.TOMLDecodeError as e:
        raise ExpectationError(f"{file}: {e}") from e
    errs: list[str] = []
    for k in doc:
        if k not in ("expect", "claim"):
            errs.append(f"no key {k}: an expectation file holds [[expect]] and [[claim]]")
    x = Expectations(file)
    for i, t in enumerate(doc.get("expect", []), 1):
        where = f"expect {i}"
        for k in t:
            if k not in _EXPECT_KEYS:
                errs.append(f"{where}: no key {k}: an expectation holds name, port or request, given, value, "
                            "ask, answer or refused, match and exact")
        e = Expectation(name=t.get("name", ""), file=file, n=i, port=t.get("port", ""), request=t.get("request", ""),
                        given=t.get("given", ""), value=t.get("value"), ask=t.get("ask"), answer=t.get("answer"),
                        refused=t.get("refused", False), exact=list(t.get("exact", [])))
        if not _NAME.match(e.name):
            errs.append(f"{where}: name {json.dumps(e.name)} is no name: lowercase words of letters, digits and "
                        "dashes, joined by dots")
        if bool(e.port) == bool(e.request):
            errs.append(f"{where}: an expectation sets port, a value it reads, or request, a request it sends, "
                        "and not both")
        elif e.port:
            if e.value is None:
                errs.append(f"{where}: port {e.port} reads no value: set value")
            if e.ask is not None or e.answer is not None or e.refused:
                errs.append(f"{where}: port {e.port} reads a value: ask, answer and refused belong to a request")
        else:
            if e.ask is None:
                errs.append(f"{where}: request {e.request} sends nothing: set ask")
            if e.value is not None:
                errs.append(f"{where}: request {e.request}: value belongs to a port; a request has ask and answer")
            if (e.answer is None) == (not e.refused):
                errs.append(f"{where}: request {e.request} relies on an answer or on its refusal: set answer, or "
                            "refused = true, and not both")
        match = t.get("match", "")
        if match == "exact":
            e.exact = [*_ROOTS, *e.exact]
        elif match not in ("", "type"):
            errs.append(f"{where}: match is type or exact, not {json.dumps(match)}")
        for p in t.get("exact", []):
            if p.split("[", 1)[0].split(".", 1)[0] not in _ROOTS:
                errs.append(f"{where}: exact path {json.dumps(p)} starts at value, ask or answer")
        x.expect.append(e)
    seen: dict[tuple[str, str], int] = {}
    for e in x.expect:
        if not e.port:
            continue
        if (e.port, e.given) in seen:
            errs.append(f"expect {e.n}: expect {seen[(e.port, e.given)]} already reads port {e.port} given "
                        f"{json.dumps(e.given)}: one value per port and given")
        seen.setdefault((e.port, e.given), e.n)
    for i, t in enumerate(doc.get("claim", []), 1):
        where = f"claim {i}"
        for k in t:
            if k not in _CLAIM_KEYS:
                errs.append(f"{where}: no key {k}: a claim holds name, and port or verb")
        c = Claim(t.get("name", ""), t.get("port", ""), t.get("verb", ""))
        if not _NAME.match(c.name):
            errs.append(f"{where}: name {json.dumps(c.name)} is no name: lowercase words of letters, digits and "
                        "dashes, joined by dots")
        if bool(c.port) == bool(c.verb):
            errs.append(f"{where}: a claim sets port, the output it writes the name on, or verb, the verb that "
                        "answers the request, and not both")
        for j, prev in enumerate(x.claims, 1):
            if prev.name == c.name:
                errs.append(f"{where}: claim {j} already claims name {json.dumps(c.name)}")
        x.claims.append(c)
    if errs:
        raise ExpectationError(f"{file}: " + "; ".join(errs))
    return x


def plain(v: Any) -> Any:
    """A TOML value as JSON holds it: its dates and times as their text."""
    if isinstance(v, list):
        return [plain(i) for i in v]
    if isinstance(v, dict):
        return {k: plain(i) for k, i in v.items()}
    if isinstance(v, (datetime.datetime, datetime.date, datetime.time)):
        return v.isoformat()
    return v


def match(root: str, want: Any, got: Any, exact: list[str] | None = None) -> None:
    """Raise where got, a value read or an answer given, does not meet want at root ("value", "ask" or
    "answer"): by type, and on the paths exact names by equality, naming the first difference's path."""
    _match(root, plain(want), json.loads(json.dumps(got)), set(exact or ()))


def _match(path: str, want: Any, got: Any, exact: set[str]) -> None:
    if path in exact:
        if not _equal(want, got):
            raise ExpectationError(f"{path} is {show(got)}, want {show(want)}")
        return
    if isinstance(want, bool):
        if not isinstance(got, bool):
            raise ExpectationError(f"{path} is {show(got)}, want true or false")
    elif isinstance(want, int):
        if not _number(got) or not float(got).is_integer():
            raise ExpectationError(f"{path} is {show(got)}, want an integer")
    elif isinstance(want, float):
        if not _number(got):
            raise ExpectationError(f"{path} is {show(got)}, want a number")
    elif isinstance(want, str):
        if not isinstance(got, str):
            raise ExpectationError(f"{path} is {show(got)}, want a string")
    elif isinstance(want, list):
        if not isinstance(got, list):
            raise ExpectationError(f"{path} is {show(got)}, want a list")
        if want and not got:
            raise ExpectationError(f"{path} is an empty list, want at least one item")
        for i, item in enumerate(got):
            first: ExpectationError | None = None
            for w in want:
                try:
                    _match(path + "[]", w, item, exact)
                    first = None
                    break
                except ExpectationError as e:
                    first = first or e
            if first is not None:
                raise ExpectationError(f"item {i}: {first}")
    elif isinstance(want, dict):
        if not isinstance(got, dict):
            raise ExpectationError(f"{path} is {show(got)}, want a table")
        for k in sorted(want):
            if k not in got:
                raise ExpectationError(f"{path}.{k} is missing")
            _match(f"{path}.{k}", want[k], got[k], exact)
    else:
        raise ExpectationError(f"{path}: the expectation holds {kind(want)}, which no value matches")


def _number(v: Any) -> bool:
    return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)


def _equal(want: Any, got: Any) -> bool:
    if isinstance(want, bool) or isinstance(got, bool):
        return want is got
    if _number(want):
        return _number(got) and float(want) == float(got)
    if isinstance(want, list):
        return isinstance(got, list) and len(got) == len(want) and all(_equal(w, g) for w, g in zip(want, got))
    if isinstance(want, dict):
        return isinstance(got, dict) and got.keys() == want.keys() and all(_equal(want[k], got[k]) for k in want)
    return want == got


def show(v: Any) -> str:
    """A value as the Go fake shows it: compact JSON, a whole float without its fraction."""
    def go(x: Any) -> Any:
        if isinstance(x, float) and x.is_integer():
            return int(x)
        if isinstance(x, list):
            return [go(i) for i in x]
        if isinstance(x, dict):
            return {k: go(i) for k, i in sorted(x.items())}
        return x
    return json.dumps(go(plain(v)), separators=(",", ":"), ensure_ascii=False)
