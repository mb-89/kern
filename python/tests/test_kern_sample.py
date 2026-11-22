"""A Python component's whole path, as its author tests it: tally, kern's sample component.

tally (``contract/fixture/tally``) declares itself in its ``component.toml``, which the
core reads at its start; its ``__init__.py`` makes it with ``kern.component`` and
decorates a derivation and its commands. Here it runs against the Python fake core,
without kern: it reads its inputs and config, commits a derivation and a command's
writes, and runs a command whose requests a continuation answers. Its attach, the
same reads and commits over the bus, and its commands through ``call.`` run against
the real core in kern's contract suite (``contract/real_test.go``).
"""

from __future__ import annotations

import sys
from pathlib import Path

KERN = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(KERN / "python"))

from kern import CONTRACT, runner  # noqa: E402
from kern.component import Ask  # noqa: E402
from kern.fake import Fake  # noqa: E402

TALLY = KERN / "contract" / "fixture" / "tally"


def test_a_python_component_declares_reads_commits_and_answers_a_command() -> None:
    # Declare: the declaration the core reads, and the hash the process attaches with.
    tally = runner.load(TALLY)
    assert tally.decl.derivations() == ["total"]
    assert sorted(tally.decl.commands) == ["tally file", "tally meet", "tally note", "tally stray", "tally wrong"]
    assert tally.decl.hash.startswith("sha256:") and CONTRACT == 1

    fake = Fake(tally)
    # Read: inputs and config by local name.
    fake.seed(numbers=[2, 3], scale=10)
    # Commit: the derivation's value, provided.
    fake.derive("total")
    assert fake.out("total").value == 50 and fake.out("total").provided
    # Command: its progress line, its text and its writes.
    assert fake.run("tally note", "hello") == 'noted "hello" beside 2 numbers'
    assert fake.lines == ["Phase: noting"]
    assert fake.out("note").value == "hello"
    # Requests and their continuation: each answered as the ledger's verb would answer it.
    lines: list[str] = []
    fake.answer("post", lambda line: lines.append(line) or len(lines))
    assert fake.run("tally file", "a", "b") == "filing 2 words\nfiled b as line 2"
    assert fake.requests == [Ask("post", "a"), Ask("post", "b")]
    assert fake.out("note").value == "b as line 2"


def test_the_sample_component_is_answered_from_its_own_expectations() -> None:
    # tally's expect.toml beside its component.toml states what it reads and what its posts are answered;
    # kern's Go fixtures counter and ledger claim and meet them (index/fixture/expect_test.go).
    from kern.component import KernError
    from kern.fake import expecting

    fake = expecting(runner.load(TALLY), TALLY / "expect.toml")
    fake.derive("total")
    assert fake.out("total").value == 0
    fake.given("numbers stand")
    fake.config(scale=10)
    fake.derive("total")
    assert fake.out("total").value == 60
    assert fake.run("tally file", "a", "b") == "filing 2 words\nfiled b as line 1"
    try:
        fake.run("tally file", "a", "")
    except KernError as e:
        assert "request 2 of 2, on post, failed: refused, as expect 4" in str(e)
    else:
        raise AssertionError("an empty line was posted")
