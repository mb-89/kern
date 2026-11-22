"""kern.record, a hot test's recordings beside an expectation file, read as kern's Go fake reads them:
kerntest/testdata/record-cases.json holds both to the same refusals and stale lines (README.md "Hot tests")."""

from __future__ import annotations

import datetime
import json
import sys
from pathlib import Path

import pytest

KERN = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(KERN / "python"))

from kern import expect, record  # noqa: E402
from kern.component import KernError  # noqa: E402
from kern.fake import expecting  # noqa: E402
from kern.pool import check  # noqa: E402

from test_kern_expect import READER_EXPECTS, reader  # noqa: E402
from test_kern_pool import modules, wiring  # noqa: E402

CASES = json.loads((KERN / "kerntest" / "testdata" / "record-cases.json").read_text())["cases"]

RECORDED = datetime.datetime(2026, 11, 22, 9, 0, tzinfo=datetime.timezone.utc)

# The lines service's real answers to READER_EXPECTS 3 to 5, as the Go hot test records them in
# kerntest/record_test.go: four lines already stood, so its first answer is 5.
READER_RECORDED = {"recordings": [
    {"expect": 3, "name": "lines.append", "request": "post", "ask": "", "hot": "lines", "at": "2026-11-22T09:00:00Z",
     "refused": True},
    {"expect": 4, "name": "lines.append", "request": "post", "ask": "a word", "hot": "lines",
     "at": "2026-11-22T09:00:00Z", "answer": 5},
    {"expect": 5, "name": "lines.append", "request": "post", "given": "two lines stand", "ask": "a word",
     "hot": "lines", "at": "2026-11-22T09:00:00Z", "answer": 3},
]}


@pytest.mark.parametrize("case", CASES, ids=[c["name"] for c in CASES])
def test_recordings_read_as_the_shared_cases_say(tmp_path: Path, case: dict) -> None:
    path = tmp_path / "reader.expect.toml"
    path.write_text(case["file"])
    Path(record.recordings_file(path)).write_text(json.dumps(case["recorded"]))

    def named(s: str) -> str:
        return s.replace("<file>", str(path)).replace("<recorded>", record.recordings_file(path))

    x = expect.read(path)
    if case["refusal"]:
        with pytest.raises(expect.ExpectationError) as e:
            record.read(x)
        assert named(case["refusal"]) in str(e.value)
        return
    recs = record.read(x)
    now = datetime.datetime.fromisoformat(case["now"].replace("Z", "+00:00"))
    assert record.stale(x, recs, now) == [named(s) for s in case["stale"]]


def recorded_reader(tmp_path: Path) -> Path:
    path = tmp_path / "reader.expect.toml"
    path.write_text(READER_EXPECTS)
    Path(record.recordings_file(path)).write_text(json.dumps(READER_RECORDED))
    return path


def test_a_readers_fake_answers_from_the_recording_and_says_where_it_is_stale(tmp_path: Path) -> None:
    path = recorded_reader(tmp_path)
    fake = expecting(reader(), path, now=RECORDED + datetime.timedelta(hours=1))
    assert fake.run("file", "x") == "filed as line 5"
    with pytest.raises(KernError, match="refused, as expect 3"):
        fake.run("file", "")
    fake.given("two lines stand")
    assert fake.run("file", "z") == "filed as line 3"
    fake.given("numbers stand")  # numbers has no recording: its port reads what the file states
    fake.derive("sum")
    assert fake.out("sum").value == 6
    assert fake.stale == []
    old = expecting(reader(), path, now=RECORDED + datetime.timedelta(days=31))
    assert len(old.stale) == 3
    assert old.stale[1] == (f'stale: expect 4 (request post, ask "a word") of {path} is answered from hot test '
                            "lines's recording of 2026-11-22T09:00:00Z, 31 days old, over 30 days: run hot test lines again")
    assert old.run("file", "x") == "filed as line 5"
    lax = expecting(reader(), path, now=RECORDED + datetime.timedelta(days=31), max_age=datetime.timedelta(days=40))
    assert lax.stale == []


def test_a_misfit_recording_is_refused_by_the_fake_and_the_check(tmp_path: Path) -> None:
    rd, count, ledger = modules(tmp_path)
    Path(record.recordings_file(rd.file)).write_text(json.dumps(READER_RECORDED))
    Path(rd.file).write_text(READER_EXPECTS.replace('ask = "a word"\nanswer = 1\n', 'ask = "a word"\nanswer = 1\nexact = ["answer"]\n', 1))
    want = ('the recording of expect 4 (request post, ask "a word") by hot test lines no longer fits it: '
            "answer is 5, want 1: run hot test lines again")
    with pytest.raises(expect.ExpectationError) as e:
        expecting(reader(), rd.file)
    assert want in str(e.value)
    with pytest.raises(expect.ExpectationError) as e:
        check(wiring("reader", "counter", "lines"), rd, count, ledger)
    assert want in str(e.value)
