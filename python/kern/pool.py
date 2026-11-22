"""The pool of expectations, keyed by name, and the components that claim and meet them, as ``kerntest``'s
``Pool``, ``Module.Meet`` and ``Check`` are in Go (README.md "Expectations").

    pool = read_pool(TALLY / "expect.toml")
    Module(counter, COUNTER / "expect.toml", given={"numbers stand": lambda f: f.config(upto=3)}).meet(pool)
    check(tomllib.loads(wiring), reader, writer)   # each expectation claimed and met by one loaded component
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

from . import expect
from .component import Component
from .fake import Fake

#: The expectations of every reader whose file was read, by the name each pools under.
Pool = dict[str, list[expect.Expectation]]
#: Sets one given precondition up in the claimer's own fake, as its own tests set its state up.
Setup = Callable[[Fake], Any]


def read_pool(*paths: str | Path) -> Pool:
    """Read the expectation files at paths into one pool."""
    pool: Pool = {}
    for p in paths:
        _add(pool, expect.read(p))
    return pool


def _add(pool: Pool, x: expect.Expectations) -> None:
    for e in x.expect:
        pool.setdefault(e.name, []).append(e)


@dataclass
class Module:
    """A component as the pool sees it: its expectation file and the setup of each given it meets."""

    component: Component
    file: str | Path | None = None
    given: dict[str, Setup] = field(default_factory=dict)

    @property
    def name(self) -> str:
        return self.component.name

    def read(self) -> expect.Expectations:
        if self.file is None:
            return expect.Expectations("")
        x = expect.read(self.file)
        x.check(self.component.decl)
        return x

    def meet(self, pool: Pool) -> None:
        """Hold the component to every expectation in pool on the names it claims; raise naming each it does
        not meet, its name, the expectation and the reader's file."""
        errs = []
        for c in self.read().claims:
            for e in pool.get(c.name, []):
                try:
                    self._meet(c, e)
                except Exception as err:  # noqa: BLE001 - every failure is the claimer's to answer
                    errs.append(self._unmet(e, err))
        if errs:
            raise expect.ExpectationError("\n".join(errs))

    def _unmet(self, e: expect.Expectation, err: Exception) -> str:
        return f'unmet: name "{e.name}": {self.name} does not meet {e} of {e.file}: {err}'

    def _meet(self, c: expect.Claim, e: expect.Expectation) -> None:
        f = Fake(self.component)
        if e.given:
            setup = self.given.get(e.given)
            if setup is None:
                raise expect.ExpectationError(f'it sets up no given "{e.given}"')
            setup(f)
        if e.port:
            if not c.port:
                raise expect.ExpectationError(f"it claims the name with verb {c.verb}, and the expectation is a value read")
            expect.match("value", e.value, _written(f, c.port), e.exact)
            return
        if not c.verb:
            raise expect.ExpectationError(f"it claims the name on output {c.port}, and the expectation is a request")
        try:
            ans = self.component.serve(c.verb, expect.plain(e.ask))
        except Exception as err:  # noqa: BLE001 - a refusal is what a verb answers
            if e.refused:
                return
            raise expect.ExpectationError(f"its verb {c.verb} refuses: {err}") from err
        if e.refused:
            raise expect.ExpectationError(f"its verb {c.verb} answers {expect.show(ans)}, and the expectation is a refusal")
        expect.match("answer", e.answer, ans, e.exact)


def _written(f: Fake, port: str) -> Any:
    """What an output reads once the setup ran: a derived one derived, a family as every key it holds."""
    o = f.component.decl.outputs[port]
    if o.derived:
        f.derive(port)
    if not o.family:
        return f.out(port).value
    return {at.split("/", 1)[1]: v for at, v in f._out.items() if at.startswith(port + "/")}
