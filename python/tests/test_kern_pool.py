"""kern.pool: a writer's test meets the pooled expectations of the names it claims, and the completeness check
holds a wiring's components to every expectation, as kerntest's Module.Meet and Check do in Go (README.md
"Expectations")."""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

KERN = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(KERN / "python"))

from kern import expect  # noqa: E402
from kern.component import Component  # noqa: E402
from kern.declaration import parse as parse_declaration  # noqa: E402
from kern.pool import Module, read_pool  # noqa: E402

from test_kern_expect import READER_EXPECTS, reader  # noqa: E402

COUNTER = b'''
name = "%s"
contract = 1
help = "Counts up."

[outputs.numbers]
type = "[int]"
builtin = []
derived = true
help = "1 to upto."

[config.upto]
type = "int"
builtin = 0
help = "How far it counts."
'''

LINES = b'''
name = "lines"
contract = 1
io = true
help = "Keeps lines."

[verbs.append]
type = "string"
answer = "int"
help = "Appends a line."
'''


def counter(name: str = "counter") -> Component:
    c = Component(parse_declaration(COUNTER % name.encode()))

    @c.derived("numbers")
    def numbers(config):
        return list(range(1, config["upto"] + 1))

    return c


def lines(kept: list[str], refuse_empty: bool = True) -> Component:
    c = Component(parse_declaration(LINES))

    @c.verb("append")
    def append(line):
        if refuse_empty and not line:
            raise ValueError("no empty line")
        kept.append(line)
        return len(kept)

    return c


def modules(tmp_path: Path) -> tuple[Module, Module, Module]:
    kept: list[str] = []
    (tmp_path / "reader.expect.toml").write_text(READER_EXPECTS)
    (tmp_path / "counter.expect.toml").write_text('[[claim]]\nname = "numbers"\nport = "numbers"\n')
    (tmp_path / "lines.expect.toml").write_text('[[claim]]\nname = "lines.append"\nverb = "append"\n')
    return (Module(reader(), tmp_path / "reader.expect.toml"),
            Module(counter(), tmp_path / "counter.expect.toml", {"numbers stand": lambda f: f.config(upto=3)}),
            Module(lines(kept), tmp_path / "lines.expect.toml", {"two lines stand": lambda f: kept.extend(["one", "two"])}))


def test_a_writers_test_meets_the_pooled_expectations_of_its_name(tmp_path: Path) -> None:
    rd, count, ledger = modules(tmp_path)
    pool = read_pool(rd.file)
    assert len(pool["numbers"]) == 2 and len(pool["lines.append"]) == 3
    count.meet(pool)
    ledger.meet(pool)


def test_a_writer_that_does_not_meet_an_expectation_fails_its_own_test(tmp_path: Path) -> None:
    rd, count, _ = modules(tmp_path)
    pool = read_pool(rd.file)
    count.given = {}
    with pytest.raises(expect.ExpectationError) as e:
        count.meet(pool)
    assert (f'unmet: name "numbers": counter does not meet expect 2 (port numbers, given "numbers stand") of '
            f'{rd.file}: it sets up no given "numbers stand"') in str(e.value)
    count.given = {"numbers stand": lambda f: None}
    with pytest.raises(expect.ExpectationError, match="value is an empty list, want at least one item"):
        count.meet(pool)
