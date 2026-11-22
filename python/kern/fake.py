"""The fake core: a component tested alone, without the core or a bus.

    fake = Fake(tally)
    fake.seed(numbers=[1, 2, 3], scale=2)    # inputs and config by local name
    fake.derive("total")
    assert fake.out("total").value == 12
    fake.answer("post", lambda line: 1)       # each request's answer, as the verb would give it
    text = fake.run("tally file", "x")
    assert fake.requests == [Ask("post", "x")]

It speaks the component contract and nothing more, as the Go fake, ``kerntest``,
does; ``contract/cases.json`` holds both, and the real core, to one suite.

A component's tests are answered from its own expectation file instead (README.md
"Expectations"), so no test fakes the component on the other side::

    fake = expecting(tally, TALLY / "expect.toml")
    fake.given("numbers stand")               # its inputs read, its requests answered, from the file
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from . import expect
from .component import Ask, Component, KernError, Result
from .declaration import CONFIG_PREFIX


@dataclass(frozen=True)
class Value:
    """What an output reads: its value, and whether the component committed it."""

    value: Any
    provided: bool


class Fake:
    """One component's inputs, committed outputs and requests, by local name."""

    def __init__(self, comp: Component):
        comp.check()
        self.component = comp
        d = comp.decl
        self._seeded: dict[str, Any] = {CONFIG_PREFIX + k: c.builtin for k, c in d.config.items()}
        self._out: dict[str, Any] = {}
        self._answers: dict[str, Callable[[Any], Any]] = {}
        #: The requests the component made and that were answered, in order.
        self.requests: list[Ask] = []
        #: The progress lines every command run so far said, in order.
        self.lines: list[str] = []
        #: Where set, the component's own expectations, which answer its inputs and requests.
        self.expectations: expect.Expectations | None = None
        self._givens: set[str] = set()

    def given(self, *givens: str) -> None:
        """Name the preconditions the test runs under, as ``Fake.Given`` does in Go: each input port reads
        the value of its expectation of one of these givens, else of the one with none, else nothing; a
        request is answered by a matching expectation of these givens first, then by one with none."""
        x = self.expectations
        if x is None:
            raise KernError(f"the fake of component {self.component.name} reads no expectations: make it with expecting")
        known = {e.given for e in x.expect}
        for g in givens:
            if not g or g not in known:
                raise KernError(f"{x.file}: no expectation is given {json.dumps(g)}")
        active = set(givens)
        chosen: dict[str, expect.Expectation] = {}
        for e in x.expect:
            if not e.port:
                continue
            prev = chosen.get(e.port)
            if not e.given and prev is None:
                chosen[e.port] = e
            elif e.given in active:
                if prev is not None and prev.given:
                    raise KernError(f"{x.file}: {prev} and {e} both read port {e.port}: name one of their givens")
                chosen[e.port] = e
        for port in self.component.decl.inputs:
            self._seeded.pop(port, None)
        for port, e in chosen.items():
            self._seeded[port] = expect.plain(e.value)
        self._givens = active

    def seed(self, values: dict[str, Any] | None = None, /, **kw: Any) -> None:
        """Set what input ports and config keys read, by local name; one never seeded reads its built-in value."""
        d = self.component.decl
        for port, v in {**(values or {}), **kw}.items():
            if port in d.inputs and self.expectations is not None:
                raise KernError(f"component {d.name} is answered from {self.expectations.file}: state what port "
                                f"{port} reads there")
            if port in d.inputs:
                self._seed_port(port, d.inputs[port], v)
            elif port in d.config:
                self._seed_port(CONFIG_PREFIX + port, d.config[port].type, v)
            else:
                raise KernError(f"component {d.name} reads no input or config key {port!r}")

    def config(self, values: dict[str, Any] | None = None, /, **kw: Any) -> None:
        """Set config keys' values; one never set is the key's built-in value."""
        d = self.component.decl
        for key, v in {**(values or {}), **kw}.items():
            if key not in d.config:
                raise KernError(f"component {d.name} has no config key {key!r}")
            self._seed_port(CONFIG_PREFIX + key, d.config[key].type, v)

    def _seed_port(self, port: str, typ: str, v: Any) -> None:
        from .declaration import fits, kind

        if not fits(v, typ):
            raise KernError(f"input {port} takes {typ}, not {kind(v)}")
        self._seeded[port] = v

    def answer(self, port: str, fn: Callable[[Any], Any]) -> None:
        """Hand a request port the function that answers every request on it; raising fails the request."""
        if self.expectations is not None:
            raise KernError(f"component {self.component.name} is answered from {self.expectations.file}: state what "
                            f"request port {port} is answered there")
        if port not in self.component.decl.requests:
            raise KernError(f"component {self.component.name} has no request port {port!r}")
        self._answers[port] = fn

    def inputs(self) -> dict[str, Any]:
        """The inputs a run reads, by local port, as the core sends them."""
        d = self.component.decl
        out = {p: self._seeded.get(p) for p in d.inputs}
        out.update({CONFIG_PREFIX + k: self._seeded[CONFIG_PREFIX + k] for k in d.config})
        return out

    def derive(self, port: str) -> None:
        """Run the derivation of an output and commit what it answers."""
        self._out[port] = self.component.derive(port, self.inputs())

    def run(self, name: str, *args: str, say: Callable[[str], None] | None = None) -> str:
        """Run a command: commit each step's writes, answer its requests in order, hand the answers to its
        continuation. A failed request stops it with its reason; it answers every step's text, one line each.
        Each line it says joins ``lines`` and goes to ``say``, the call's own caller, where given: calls
        that run side by side, each in its own thread, so each hear their own."""
        text: list[str] = []

        def said(line: str) -> None:
            self.lines.append(line)
            if say is not None:
                say(line)

        try:
            r: Result = self.component.run(name, list(args), self.inputs(), said)
        except Exception as e:
            raise KernError(str(e)) from e
        while True:
            if r.text:
                text.append(r.text)
            for w in r.writes:
                at = _at(w.port, w.key)
                if w.gone:
                    self._out.pop(at, None)
                else:
                    self._out[at] = w.value
            answers = []
            for i, a in enumerate(r.requests, 1):
                try:
                    ans = self._answer(a)
                except Exception as e:
                    raise KernError(f"{name}: request {i} of {len(r.requests)}, on {a.port}, failed: {e}", "\n".join(text)) from e
                self.requests.append(a)
                answers.append(ans)
            if r.then is None:
                return "\n".join(text)
            try:
                r = self.component.then(r, answers)
            except Exception as e:
                raise KernError(str(e), "\n".join(text)) from e

    def _answer(self, a: Ask) -> Any:
        from .declaration import fits, kind

        if self.expectations is not None:
            return self._expected(a)
        fn = self._answers.get(a.port)
        if fn is None:
            raise KernError(f"request port {a.port} has no answer: hand it one with answer")
        v = fn(a.value)
        want = self.component.decl.requests[a.port].answer
        if not fits(v, want):
            raise KernError(f"request port {a.port} reads {want}, and its answerer gave {kind(v)}")
        return v

    def _expected(self, a: Ask) -> Any:
        """A request's answer from the expectations: the first whose ask it matches, of the test's givens first,
        then of none."""
        x = self.expectations
        assert x is not None
        for mine in (lambda e: e.given in self._givens, lambda e: not e.given):
            for e in x.expect:
                if e.request != a.port or not mine(e):
                    continue
                try:
                    expect.match("ask", e.ask, a.value, e.exact)
                except expect.ExpectationError:
                    continue
                if e.refused:
                    raise KernError(f"refused, as {e} of {x.file} expects")
                return expect.plain(e.answer)
        raise KernError(f"{x.file}: no expectation answers the request {expect.show(a.value)} on port {a.port}: "
                        "state it there")

    def out(self, port: str, key: str = "") -> Value:
        """What the component committed to an output, at key for a family, or its built-in value."""
        o = self.component.decl.outputs.get(port)
        if o is None:
            raise KernError(f"component {self.component.name} writes no output {port!r}")
        if o.family != bool(key):
            raise KernError(f"output {port} of component {self.component.name}: a family is read at a key, and only a family")
        at = _at(port, key)
        if at in self._out:
            return Value(self._out[at], True)
        return Value(o.builtin, False)


def expecting(comp: Component, path: str | Path) -> Fake:
    """A fake that answers comp's tests from its expectation file, path, as ``kerntest.Expecting`` does: seed
    and answer are refused, so no test fakes the component on the other side."""
    f = Fake(comp)
    x = expect.read(path)
    x.check(comp.decl)
    f.expectations = x
    f.given()
    return f


def _at(port: str, key: str) -> str:
    return f"{port}/{key}" if key else port
