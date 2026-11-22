"""kern.pool's pending expectations: a new expectation is pending until its claimer first meets it, its claimer's
test reports it without failing and its reader's check refuses it, as kerntest's are in Go (README.md "Pending
expectations")."""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

KERN = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(KERN / "python"))

from kern import expect  # noqa: E402
from kern.pool import REPORT_VARIABLE, check, git_base, read_pool  # noqa: E402

from test_kern_expect import READER_EXPECTS  # noqa: E402
from test_kern_pool import modules, wiring  # noqa: E402

# The reader's new wish: four numbers, given a precondition counter sets up nowhere.
FOUR_NUMBERS = '''
[[expect]]
name = "numbers"
port = "numbers"
given = "four numbers stand"
value = [1, 2, 3, 4]
match = "exact"
'''


def landed_as(path: Path, text: str):
    """The base that holds the reader's file as text."""

    def base(p: str):
        if p != str(path):
            return expect.Expectations(p), False
        return expect.parse(p, text.encode()), True

    return base


def new_wish(tmp_path: Path):
    rd, count, ledger = modules(tmp_path)
    Path(rd.file).write_text(READER_EXPECTS + FOUR_NUMBERS)
    return rd, count, ledger, landed_as(rd.file, READER_EXPECTS)


def pending_line(file) -> str:
    return (f'pending: name "numbers": expect 6 (port numbers, given "four numbers stand") of {file} waits on '
            'counter, which does not meet it yet')


def test_a_new_unmet_expectation_is_pending_in_its_claimers_test(tmp_path: Path) -> None:
    rd, count, ledger, base = new_wish(tmp_path)
    pool = read_pool(rd.file, base=base)
    pending = count.meet(pool)
    assert [str(p) for p in pending] == [pending_line(rd.file) + ': it sets up no given "four numbers stand"']
    assert ledger.meet(pool) == []


def test_a_new_unmet_expectation_fails_its_readers_check_naming_the_claimer(tmp_path: Path) -> None:
    rd, count, ledger, base = new_wish(tmp_path)
    with pytest.raises(expect.ExpectationError) as e:
        check(wiring("reader", "counter", "lines"), rd, count, ledger, base=base)
    assert pending_line(rd.file) in str(e.value)
    assert "unmet:" not in str(e.value)


def test_once_its_claimer_meets_it_failing_it_fails_the_claimer(tmp_path: Path) -> None:
    rd, count, _, base = new_wish(tmp_path)
    count.given["four numbers stand"] = lambda f: f.config(upto=4)
    assert count.meet(read_pool(rd.file, base=base)) == []
    pool = read_pool(rd.file, base=landed_as(rd.file, READER_EXPECTS + FOUR_NUMBERS))
    count.given["four numbers stand"] = lambda f: f.config(upto=5)
    with pytest.raises(expect.ExpectationError) as e:
        count.meet(pool)
    assert (f'unmet: name "numbers": counter does not meet expect 6 (port numbers, given "four numbers stand") of '
            f'{rd.file}') in str(e.value)


def test_an_expectation_stays_landed_where_it_moves_in_its_file(tmp_path: Path) -> None:
    rd, _, _ = modules(tmp_path)
    at = READER_EXPECTS.index('[[expect]]\nname = "lines.append"')
    Path(rd.file).write_text(READER_EXPECTS[at:] + READER_EXPECTS[:at])
    pool = read_pool(rd.file, base=landed_as(rd.file, READER_EXPECTS))
    assert not [e for es in pool.values() for e in es if e.new]


def test_with_no_base_every_expectation_counts_as_landed(tmp_path: Path) -> None:
    rd, count, _, _ = new_wish(tmp_path)
    with pytest.raises(expect.ExpectationError, match="unmet:"):
        count.meet(read_pool(rd.file, base=None))


def test_a_pending_expectation_is_appended_to_the_report_file(tmp_path: Path, monkeypatch) -> None:
    report = tmp_path / "pending.jsonl"
    monkeypatch.setenv(REPORT_VARIABLE, str(report))
    rd, count, ledger, base = new_wish(tmp_path)
    count.meet(read_pool(rd.file, base=base))
    with pytest.raises(expect.ExpectationError):
        check(wiring("reader", "counter", "lines"), rd, count, ledger, base=base)
    lines = [json.loads(line) for line in report.read_text().splitlines()]
    assert len(lines) == 2
    for r in lines:
        assert (r["claimer"], r["name"], Path(r["file"]), r["n"]) == ("counter", "numbers", Path(rd.file).absolute(), 6)
        assert r["line"].startswith("pending: ")


def test_the_git_base_is_the_readers_file_as_the_revision_holds_it(tmp_path: Path) -> None:
    path = tmp_path / "reader.expect.toml"
    answers: dict[str, str] = {}

    def git(folder: str, args: list[str]) -> tuple[int, str]:
        key = " ".join(args)
        return (0, answers[key]) if key in answers else (1, "")

    assert git_base("origin/main", git)(str(path))[1] is False
    answers["rev-parse --show-toplevel"] = str(tmp_path) + "\n"
    answers["rev-parse --verify --quiet origin/main^{commit}"] = "abc\n"
    with pytest.raises(expect.ExpectationError, match="KERN_EXPECT_BASE=origin/gone: no commit origin/gone"):
        git_base("origin/gone", git)(str(path))
    answers["ls-tree --name-only origin/main -- reader.expect.toml"] = ""
    x, known = git_base("origin/main", git)(str(path))
    assert known and x.expect == []
    answers["ls-tree --name-only origin/main -- reader.expect.toml"] = "reader.expect.toml\n"
    answers["show origin/main:./reader.expect.toml"] = READER_EXPECTS
    x, known = git_base("origin/main", git)(str(path))
    assert known and len(x.expect) == 5 and x.file == str(path)
