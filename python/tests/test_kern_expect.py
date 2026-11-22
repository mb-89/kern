"""kern.expect, the expectation file, read as kern's Go fake reads it: kerntest/testdata/expect-cases.json
holds both to the same refusals and matches (README.md "Expectations")."""

from __future__ import annotations

import json
import re
import sys
import tomllib
from pathlib import Path

import pytest

KERN = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(KERN / "python"))

from kern import expect  # noqa: E402
from kern.declaration import parse as parse_declaration  # noqa: E402

CASES = json.loads((KERN / "kerntest" / "testdata" / "expect-cases.json").read_text())


def test_an_expectation_file_reads_its_expectations_and_claims() -> None:
    x = expect.parse("reader.expect.toml", b'''
[[expect]]
name = "numbers"
port = "numbers"
given = "numbers stand"
value = [1, 2, 3]

[[expect]]
name = "lines.append"
request = "post"
ask = ""
refused = true
exact = ["ask"]

[[expect]]
name = "lines.append"
request = "post"
ask = "a word"
answer = 1
match = "exact"

[[claim]]
name = "note"
port = "note"
''')
    assert len(x.expect) == 3 and x.claims == [expect.Claim("note", port="note")]
    assert str(x.expect[0]) == 'expect 1 (port numbers, given "numbers stand")'
    assert str(x.expect[1]) == 'expect 2 (request post, ask "")'
    assert x.expect[2].exact == ["value", "ask", "answer"]


@pytest.mark.parametrize("case", CASES["refused"], ids=lambda c: c["refusal"][:40])
def test_an_expectation_file_refuses_what_is_malformed(case: dict) -> None:
    with pytest.raises(expect.ExpectationError) as e:
        expect.parse("bad.expect.toml", case["file"].encode())
    assert str(e.value).startswith("bad.expect.toml") and case["refusal"] in str(e.value)


@pytest.mark.parametrize("case", CASES["matches"], ids=lambda c: f"{c['want']} {c['got']}")
def test_an_expectation_matches_by_type_and_on_its_exact_paths_by_equality(case: dict) -> None:
    want = tomllib.loads(case["want"])["value"]
    if not case["refusal"]:
        expect.match("value", want, case["got"], case["exact"])
        return
    with pytest.raises(expect.ExpectationError) as e:
        expect.match("value", want, case["got"], case["exact"])
    assert str(e.value) == case["refusal"]


def test_an_expectation_file_is_held_to_its_components_declaration() -> None:
    d = parse_declaration(b'''
name = "reader"
contract = 1
help = "Reads numbers and posts lines."

[inputs]
numbers = "[int]"

[outputs.sum]
type = "int"
builtin = 0
derived = true
help = "The sum."

[requests.post]
type = "string"
answer = "int"
help = "Posts a line."
''')
    x = expect.parse("reader.expect.toml", b'''
[[expect]]
name = "numbers"
port = "numbers"
value = ["one"]

[[expect]]
name = "numbers"
port = "elsewhere"
value = [1]

[[expect]]
name = "lines.append"
request = "post"
ask = 1
answer = "one"

[[expect]]
name = "lines.append"
request = "mail"
ask = "x"
answer = 1

[[claim]]
name = "total"
port = "total"

[[claim]]
name = "lines.append"
verb = "append"
''')
    with pytest.raises(expect.ExpectationError) as e:
        x.check(d)
    for want in ('expect 1 (port numbers): value: ["one"] is no [int]',
                 'expect 2 (port elsewhere): component reader reads no input "elsewhere"',
                 "expect 3 (request post, ask 1): ask: 1 is no string",
                 'answer: "one" is no int',
                 'component reader has no request port "mail"',
                 'claim 1: component reader writes no output "total"',
                 'claim 2: component reader accepts no verb "append"'):
        assert want in str(e.value)


READER = b'''
name = "reader"
contract = 1
help = "Sums numbers and posts lines."

[inputs]
numbers = "[int]"

[outputs.sum]
type = "int"
builtin = 0
derived = true
help = "The sum."

[commands.file]
usage = "file WORDS..."
summary = "File words."
group = "Tests"
help = "Posts each word."
class = "batch"

[requests.post]
type = "string"
answer = "int"
help = "Posts a line."
'''

READER_EXPECTS = '''
[[expect]]
name = "numbers"
port = "numbers"
value = []
match = "exact"

[[expect]]
name = "numbers"
port = "numbers"
given = "numbers stand"
value = [1, 2, 3]

[[expect]]
name = "lines.append"
request = "post"
ask = ""
exact = ["ask"]
refused = true

[[expect]]
name = "lines.append"
request = "post"
ask = "a word"
answer = 1

[[expect]]
name = "lines.append"
request = "post"
given = "two lines stand"
ask = "a word"
answer = 3
'''


def reader():
    from kern.component import Component

    c = Component(parse_declaration(READER))

    @c.derived("sum")
    def total(numbers, config):
        return sum(numbers or [])

    @c.command("file")
    def file(args, inputs, say):
        return c.result(requests=[c.ask("post", a) for a in args], then=lambda answers: f"filed as line {answers[-1]}")

    return c


def test_a_readers_test_is_answered_from_its_own_expectations(tmp_path: Path) -> None:
    from kern.component import KernError
    from kern.fake import expecting

    path = tmp_path / "reader.expect.toml"
    path.write_text(READER_EXPECTS)
    fake = expecting(reader(), path)
    fake.derive("sum")
    assert fake.out("sum").value == 0
    fake.given("numbers stand")
    fake.derive("sum")
    assert fake.out("sum").value == 6
    assert fake.run("file", "x", "y") == "filed as line 1"
    with pytest.raises(KernError, match=re.escape(f'refused, as expect 3 (request post, ask "") of {path} expects')):
        fake.run("file", "")
    fake.given("two lines stand")
    assert fake.run("file", "z") == "filed as line 3"
    fake.derive("sum")
    assert fake.out("sum").value == 0
    with pytest.raises(KernError, match="is answered from"):
        fake.seed(numbers=[9])
    with pytest.raises(KernError, match="is answered from"):
        fake.answer("post", lambda line: 9)
    with pytest.raises(KernError, match='no expectation is given "the ledger burned"'):
        fake.given("the ledger burned")


def test_a_request_no_expectation_answers_fails_the_readers_test(tmp_path: Path) -> None:
    from kern.component import KernError
    from kern.fake import expecting

    path = tmp_path / "reader.expect.toml"
    path.write_text('[[expect]]\nname = "lines.append"\nrequest = "post"\nask = "only this"\nexact = ["ask"]\nanswer = 1\n')
    fake = expecting(reader(), path)
    with pytest.raises(KernError, match='no expectation answers the request "that" on port post: state it there'):
        fake.run("file", "that")
    bad = tmp_path / "bad.expect.toml"
    bad.write_text('[[expect]]\nname = "n"\nport = "nowhere"\nvalue = 1\n')
    with pytest.raises(expect.ExpectationError, match='reads no input "nowhere"'):
        expecting(reader(), bad)
