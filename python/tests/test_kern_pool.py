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
from kern.pool import Module, check, read_pool  # noqa: E402

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


def wiring(*components: str) -> dict:
    instances = {}
    for c in components:
        name, off = c.removesuffix(" off"), c.endswith(" off")
        instances[name] = {"component": name, **({"enabled": False} if off else {})}
    return {"instances": instances}


def test_the_completeness_check_passes_where_each_expectation_is_claimed_and_met_once(tmp_path: Path) -> None:
    rd, count, ledger = modules(tmp_path)
    check(wiring("reader", "counter", "lines"), rd, count, ledger)
    other = Module(counter("other-counter"), count.file, count.given)
    check(wiring("reader", "other-counter", "lines"), rd, count, other, ledger)
    check(wiring("reader", "counter", "other-counter off", "lines"), rd, count, other, ledger)


def test_the_completeness_check_refuses_an_unclaimed_expectation(tmp_path: Path) -> None:
    rd, count, ledger = modules(tmp_path)
    with pytest.raises(expect.ExpectationError) as e:
        check(wiring("reader", "counter", "lines off"), rd, count, ledger)
    assert f'unclaimed: name "lines.append": no loaded component claims expect 3 (request post, ask "") of {rd.file}' in str(e.value)
    assert 'name "numbers"' not in str(e.value)


def test_the_completeness_check_refuses_an_expectation_claimed_twice(tmp_path: Path) -> None:
    rd, count, ledger = modules(tmp_path)
    other = Module(counter("other-counter"), count.file, count.given)
    with pytest.raises(expect.ExpectationError) as e:
        check(wiring("reader", "counter", "other-counter", "lines"), rd, count, other, ledger)
    assert (f'claimed twice: name "numbers": counter and other-counter, all loaded, claim expect 1 (port numbers) '
            f'of {rd.file}: exactly one may') in str(e.value)


def test_the_completeness_check_refuses_an_unmet_expectation(tmp_path: Path) -> None:
    rd, count, ledger = modules(tmp_path)
    ledger.component = lines([], refuse_empty=False)
    with pytest.raises(expect.ExpectationError) as e:
        check(wiring("reader", "counter", "lines"), rd, count, ledger)
    assert (f'unmet: name "lines.append": lines does not meet expect 3 (request post, ask "") of {rd.file}: its verb '
            "append answers 1, and the expectation is a refusal") in str(e.value)
