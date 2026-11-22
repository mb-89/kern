# kern's rules for agents

kern is the Go core every application builds on ([README.md](README.md)). It is worked by SE's
roles; SE's agent definitions and its work model are the rules of how, and this file names what is
kern's own. `<se>` is where SE lives: the start hook names it as `SE root: <path>`; in CI it is
`.se/ci/se`, SE checked out at the commit `se.lock` locks. `.se/` is git-ignored.

## Tests

```sh
go vet ./... && go test ./...
uv run --no-project --python ">=3.11" python <se>/scripts/work_tickets.py suite --worktree .
```

- `se.toml` `[tests]` is kern's Go tests, held to their budget; beside them `[tests.beside.python]`
  (the Python SDK's tests in `python/tests`, through the root's `pyproject.toml`),
  `[tests.beside.template]` (the bare application's tests) and `[tests.beside.release-guard]`.
- The focused tests are the packages a change touches, `go test ./<package>/`, and `uv run pytest
  -q` for a change under `python/`; the complete suite is `work_tickets.py suite`.
- No test waits on the wall clock: a time limit under test is shortened or faked (README.md
  "Tests"), never waited out.
- `gofmt -l $(git ls-files '*.go')` lists nothing: `check` fails on any file it lists.
- `.github/workflows/check.yml` is the check `check` the ruleset requires (README.md "Tests").

## Hooks

```sh
<se>/scripts/setup.sh <kern checkout>
```

installs SE's git hooks into kern's checkout: the public check runs on every commit and push, and
the checkout's commit identity is set to `se.toml`'s `identity`, `mb <mb>`. Run it in every fresh
clone before its first commit. Never bypass a hook.

## The neutral home

No tracked file names where kern is hosted: not its host, its owner or its repository. The module
path stays `kern`, which names no host. Where a file needs the repository's location (the release's
download, the repository a release is published into), it derives it from the checkout's origin
when it runs (`home/`). A link out of the repository is plain text, such as SE's
`docs/go-core.md` "A call given up", never a Markdown link; `kerntest/moveout_test.go` lists what
still names SE or one host (`kerntest/testdata/names-se-or-host.txt`).

## The release

`release.toml` owns kern's version and its tag, `v<version>` under kern's own releases
(README.md "Release"); `se.toml` names no version. `.github/workflows/release.yml` publishes the
release on the push to `main` that changes the version, and never changes one that exists. A
landing assigns the version from the field report's `**Component kinds:** kern <kind>`, through
`se.toml` `[binary]`; the release guard (`go run ./tools/release guard --dir .`) refuses a change
to the binary's inputs without it.

## The landing

Work lands in kern as it lands in SE: `se.toml` `[integrate] landing = "fast-forward"`, so `main`
moves to the tested landing commit, the integrator's `mb <mb>` commit that `check` passed, and no
commit on `main` carries an address (SE's README "Landing on main"). Commit messages carry no
tracker reference, no address and no trailer.

`check` runs SE's tools at kern's SE pin, `se.toml` `[modules.se]` locked in `se.lock`:
`<se>/scripts/se_modules.py --root . sync se` writes it and `update se` moves it.
An SE landing therefore cannot fail kern's pull requests.

## What belongs in kern

SE's `docs/go-core.md`, "The core's repository":

> **What belongs in it:** what every application needs, and nothing that names an application:
> the core, the bus, the fakes, the contract suite, the Python SDK, the template, the reusable CI,
> and the generic components.
>
> **What stays in an application:** every component that knows its domain. SE's work issues,
> field reports, fleet check, retro and review stay in SE.
