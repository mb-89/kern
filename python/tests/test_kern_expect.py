"""kern.expect, the expectation file, read as kern's Go fake reads it: kerntest/testdata/expect-cases.json
holds both to the same refusals and matches (README.md "Expectations")."""

from __future__ import annotations

import json
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
