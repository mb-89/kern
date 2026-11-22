"""The pool of expectations, keyed by name, and the components that claim and meet them, as ``kerntest``'s
``Pool``, ``Module.Meet`` and ``Check`` are in Go (README.md "Expectations").

    pool = read_pool(TALLY / "expect.toml")
    Module(counter, COUNTER / "expect.toml", given={"numbers stand": lambda f: f.config(upto=3)}).meet(pool)
    check(tomllib.loads(wiring), reader, writer)   # each expectation claimed and met by one loaded component

A new expectation is pending until its claimer first meets it, as in Go (README.md "Pending expectations"):
new where the reader's file at the base, the git revision ``KERN_EXPECT_BASE`` names, holds none with its
terms; pending where new and unmet now. ``meet`` answers the pending ones without failing; ``check`` refuses
them, naming the claimer.
"""

from __future__ import annotations

import json
import os
import subprocess
import threading
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import Any, Callable

from . import expect, record
from .component import Component
from .fake import Fake

#: The expectations of every reader whose file was read, by the name each pools under.
Pool = dict[str, list[expect.Expectation]]
#: Sets one given precondition up in the claimer's own fake, as its own tests set its state up.
Setup = Callable[[Fake], Any]


#: The git revision a process reads its pools against; unset, every expectation counts as landed.
BASE_VARIABLE = "KERN_EXPECT_BASE"
#: The file each pending expectation is appended to, one JSON line each, for ``go run ./tools/expect pending``.
REPORT_VARIABLE = "KERN_PENDING"

#: Answers an expectation file as the base holds it, and whether the base can know the file at all.
Base = Callable[[str], "tuple[expect.Expectations, bool]"]
#: Runs git with args in a folder: its exit code and standard output.
Git = Callable[[str, list[str]], "tuple[int, str]"]

_ENVIRONMENT: Any = object()


def _git(folder: str, args: list[str]) -> tuple[int, str]:
    try:
        r = subprocess.run(["git", *args], cwd=folder, capture_output=True, text=True, check=False)
    except OSError:
        return -1, ""
    return r.returncode, r.stdout


def git_base(ref: str, git: Git = _git) -> Base:
    """The base the revision ref holds: a file as ref holds it. A file no repository holds is not known, so its
    expectations count as landed; a file ref lacks is known and holds none."""

    def base(path: str) -> tuple[expect.Expectations, bool]:
        p = Path(path).absolute()
        folder, name = str(p.parent), p.name
        if git(folder, ["rev-parse", "--show-toplevel"])[0] != 0:
            return expect.Expectations(path), False
        if git(folder, ["rev-parse", "--verify", "--quiet", ref + "^{commit}"])[0] != 0:
            raise expect.ExpectationError(f"{BASE_VARIABLE}={ref}: no commit {ref}")
        code, held = git(folder, ["ls-tree", "--name-only", ref, "--", name])
        if code != 0:
            raise expect.ExpectationError(f"git ls-tree {ref} -- {name} failed in {folder}")
        if not held.strip():
            return expect.Expectations(path), True
        code, text = git(folder, ["show", f"{ref}:./{name}"])
        if code != 0:
            raise expect.ExpectationError(f"git show {ref}:./{name} failed in {folder}")
        return expect.parse(path, text.encode()), True

    return base


def _base(base: Any) -> Base | None:
    if base is not _ENVIRONMENT:
        return base
    ref = os.environ.get(BASE_VARIABLE, "")
    return git_base(ref) if ref else None


def _since(base: Base | None, x: expect.Expectations) -> expect.Expectations:
    """Mark each expectation of x the base does not hold as new."""
    if base is None or not x.expect:
        return x
    landed, known = base(x.file)
    if not known:
        return x
    held = {e.terms() for e in landed.expect}
    x.expect = [replace(e, new=e.terms() not in held) for e in x.expect]
    return x


@dataclass(frozen=True)
class Pending:
    """A new expectation its claimer does not meet yet: it blocks its reader's check, and its claimer's test
    reports it without failing."""

    expectation: expect.Expectation
    claimer: str
    why: str

    def __str__(self) -> str:
        e = self.expectation
        return f'pending: name "{e.name}": {e} of {e.file} waits on {self.claimer}, which does not meet it yet: {self.why}'


_REPORT = threading.Lock()


def _report(p: Pending) -> None:
    path = os.environ.get(REPORT_VARIABLE, "")
    if not path:
        return
    e = p.expectation
    line = json.dumps({"claimer": p.claimer, "name": e.name, "file": str(Path(e.file).absolute()), "n": e.n,
                       "line": str(p)})
    with _REPORT, open(path, "a", encoding="utf-8") as f:
        f.write(line + "\n")


def read_pool(*paths: str | Path, base: Base | None = _ENVIRONMENT) -> Pool:
    """Read the expectation files at paths into one pool, each against base: by default the one
    ``KERN_EXPECT_BASE`` names, if any; None holds every expectation."""
    b = _base(base)
    pool: Pool = {}
    for p in paths:
        _add(pool, _since(b, expect.read(p)))
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

    def meet(self, pool: Pool) -> list[Pending]:
        """Hold the component to every expectation in pool on the names it claims; raise naming each it does
        not meet, its name, the expectation and the reader's file, except a new one, which is pending: answer
        those, for its test to report, and append them to ``KERN_PENDING``'s file, where it is set."""
        errs, pending = [], []
        for c in self.read().claims:
            for e in pool.get(c.name, []):
                try:
                    self._meet(c, e)
                except Exception as err:  # noqa: BLE001 - every failure is the claimer's to answer
                    if e.new:
                        pending.append(Pending(e, self.name, str(err)))
                        _report(pending[-1])
                    else:
                        errs.append(self._unmet(e, err))
        if errs:
            raise expect.ExpectationError("\n".join(errs))
        return pending

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


def check(wiring: dict, *mods: Module, base: Base | None = _ENVIRONMENT) -> None:
    """The completeness check over a wiring, as ``kerntest.Check``: among the components its switched-on
    instances load, every expectation of a loaded component is claimed by exactly one loaded component and met
    by it, and every recording beside a loaded component's file still fits its expectation. Raise naming each that
    is unclaimed, claimed twice, unmet or pending, and each misfit recording, each in its own words; a pending one
    names the claimer that must meet it. Each reader's file is read against base, as ``read_pool``; a recording is
    of the file as it stands, pending or not."""
    b = _base(base)
    loaded = {t["component"] for t in wiring.get("instances", {}).values() if t.get("enabled", True)}
    by_name: dict[str, Module] = {}
    for m in mods:
        if m.name in by_name:
            raise expect.ExpectationError(f"the check takes component {m.name} twice")
        if m.name in loaded:
            by_name[m.name] = m
    pool: Pool = {}
    claims: dict[str, list[tuple[Module, expect.Claim]]] = {}
    errs = []
    for n in sorted(by_name):
        x = by_name[n].read()
        try:
            record.read(x)
        except expect.ExpectationError as err:
            errs.append(str(err))
        x = _since(b, x)
        _add(pool, x)
        for c in x.claims:
            claims.setdefault(c.name, []).append((by_name[n], c))
    for e in sorted((e for es in pool.values() for e in es), key=lambda e: (e.file, e.n)):
        cs = claims.get(e.name, [])
        if not cs:
            errs.append(f'unclaimed: name "{e.name}": no loaded component claims {e} of {e.file}')
        elif len(cs) > 1:
            names = " and ".join(m.name for m, _ in cs)
            errs.append(f'claimed twice: name "{e.name}": {names}, all loaded, claim {e} of {e.file}: exactly one may')
        else:
            m, c = cs[0]
            try:
                m._meet(c, e)
            except Exception as err:  # noqa: BLE001 - every failure is the claimer's to answer
                if e.new:
                    p = Pending(e, m.name, str(err))
                    errs.append(str(p))
                    _report(p)
                else:
                    errs.append(m._unmet(e, err))
    if errs:
        raise expect.ExpectationError("\n".join(errs))
