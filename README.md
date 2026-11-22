# kern

kern is the Go core every application builds on: the index (names, the store, snapshots, the
catalog), its NATS bus and its host. Its design is SE's
`docs/go-core.md` until kern owns its contract.

## Layout

The design's layout ("The core's repository"), relative to `kern/`; the parts later steps add are
named in the design's plan.

| path | what | from |
|---|---|---|
| `go.mod` | the module and its pinned Go version, which CI reads | |
| `index/` | names, families, the store, snapshots, a single writer per name, built-in values, the catalog check at start, waves, actions, the collector, and `wiring.toml` read at start | `src/index` at `ff01204` |
| `index/fixture/` | the fixture components the index and the fake core are tested with, their expectation files ([Expectations](#expectations)), and the recordings a hot test wrote beside the watcher's ([Hot tests](#hot-tests)) | `src/index/fixture` |
| `kerntest/` | the Go fake core: seed by local name, run, assert writes and requests, answer each request, give a call up as the core does (`Begin`, `BeginServe`, `Abandon`, `Gone`); answer a component from its expectations, meet the pooled ones it claims, and check a wiring's completeness ([Expectations](#expectations)); an I/O module's hot test and the recordings it writes ([Hot tests](#hot-tests)) | `src/index/indextest` |
| `bus/` | the embedded NATS server on loopback, a user and permissions per component instance, so the bus refuses a second writer | quackitect v5 `src/index/bus.go` |
| `host/` | the core's process: the standing file, the detached start and its claim file, leases, placements, the idle end | v5 `src/index/detach*.go`, `start.go`, `door.go` |
| `git/` | kern's git door: every git command kern and SE's Go components run, real git outside tests and the fake (`git/gitfake/`) in them, both held to one contract (`git/contract_test.go`); a test that chose no backend is refused real git, and `SE_GIT_DOOR` picks a process's backend, its `fake:<state file>` read from the state SE's Python fake writes (`git/gitfake/state.go`) | SE's `scripts/git_door.py` |
| `cli/` | the core's command line, the same for the bare core and every application: `<name> start`, `status`, `stop`, `serve`, `hook`, `mcp`, `lint`, `lsp` | |
| `mcp/` | the MCP door, `<name> mcp`: the catalog's read-only and writing commands as tools over stdio ([The MCP door](#the-mcp-door)) | |
| `lint/` | the one checker of managed files: each file's template or schema, links that reach nothing, names over their caps; the language server, `<name> lint` and the tests ask it ([Language server](#language-server)) | quackitect v5 `Sweep` |
| `lsp/` | the language server, `<name> lsp`: the checker's findings as diagnostics over stdio, which an editor starts ([Language server](#language-server)) | quackitect v5 `spec/design_output/lsp.md` |
| `form/` | the templates: `<name>.json`, its fields' shapes and schema, which the GitHub component fills and renders and the checker holds a rendered file to | `components/github/template.go` |
| `cmd/kern/` | the bare binary: `kern start`, `kern status`, `kern stop`, `kern serve`, `kern hook`, `kern lint`, `kern lsp`, carrying kern's shared components | |
| `components/clock/` | the clock: time as names, `time` since its start 10 times a second, and `second`, `minute` and `hour` counting up, which a component reads instead of keeping a timer ([its README](components/clock/README.md)) | |
| `components/files/` | the files component: the managed folders held in memory as names, reads and searches from the model, compare-and-swap writes, a watcher and a hash reconcile ([Files](#files), [its README](components/files/README.md)) | quackitect v4 `src/engine/indexwatch.go`, v5 `src/modules/files/watch.go` |
| `components/lsp/` | the lsp component: the checker over the warm model, its findings a name in step with every managed file, and `lsp lint`, which the language server, `<name> lint` and the tests ask ([Language server](#language-server), [its README](components/lsp/README.md)) | quackitect v5 `lsp` I/O module |
| `components/github/` | the GitHub component: the token and relay backends and the snapshot names ([its README](components/github/README.md)) | the design's step 7 |
| `components/session/` | the session record, off by default: one JSON line per host event, a file per session under `.se/.runtime/sessions/` ([its README](components/session/README.md)) | |
| `contract/` | the contract suite: the built binary held to the bus's and the host's promises, and `cases.json` held against the Go fake, the Python fake and the real core with a Python component attached | |
| `contract/fixture/` | tally, the suite's fixture and the sample Python component (`component.toml`, `__init__.py`), and the declarations of the two peers the real leg plays | |
| `python/kern/` | the Python SDK, standard library only: the NATS peer, `component.toml`'s reader, the component and its decorators, the fake core, expectations and their pool (`expect.py`, `pool.py`), a hot test's recordings (`record.py`), the runner `python -m kern run <folder>` | |
| `python/tests/` | the Python fake's leg of the contract suite and the sample component's test, run by `se.toml` `[tests.beside.python]` | |
| `release.toml` | kern's version and the one URL its release is published under ([Release](#release)) | |
| `tools/release/` | builds the release: six archives and `SHA256SUMS` | |
| `forbid/` | the scan that holds every I/O module to its forbid list: the Go and Python code under `components/` that uses another I/O module's entry ([Forbid lists](#forbid-lists)) | |
| `tools/expect/` | `pending`: lists the pending expectations per claimer ([Pending expectations](#pending-expectations)) | |
| `template/` | the bare application every new application starts from ([The template](#the-template)) | |

The NATS server is a Go dependency, `github.com/nats-io/nats-server/v2` v2.11.9 with
`github.com/nats-io/nats.go` v1.45.0, v5's versions; `go.mod` owns them.

## The host

One core runs per checkout, the nearest folder up from the working directory that holds `.git`
(or `--root`). Nobody needs to start it by hand: a hook runs `kern start`, which returns at once.
The verbs are one table in `cli/verbs.go`; an application adds its own with `cli.Add` and its
bare run with `cli.Bare`, and the help renders them all.

| command | what |
|---|---|
| `kern help [<verb>]` | prints the whole help on stdout and exits 0, as `--help` and `-h` do: every verb with its arguments, those hosts and hooks call under their own heading, and the exit codes; `kern <verb> -h` prints that verb's options; a bare `kern` prints the usage line on stderr and exits 2, unless the application sets a bare run (`cli.Bare`, as `se` does) |
| `kern start [--grace D] [--idle D]` | where no core runs and no other caller's claim stands, claims the start (`.se/.runtime/start.claim`) and spawns `kern serve` detached; says `started`, `starting` or `running` |
| `kern status [--wait D] [--json]` | `running` with the health (`get.core.health`, 200 ms wait), `starting`, or `not running` (exit 1) |
| `kern stop` | asks `call.core.stop` and waits until the process has exited |
| `kern serve [--grace D] [--idle D]` | the core in the foreground: reads `wiring.toml` where the checkout has one |
| `kern mcp [--grace D] [--idle D]` | the MCP door over stdio ([The MCP door](#the-mcp-door)): starts the core with the grace and idle given, or attaches to it, and holds a session lease while the client is connected |
| `kern mcp --tools` | prints the tools the door would serve for the checkout, as `tools/list` answers them, indented JSON in served order, and exits 0: starts no core, takes no lease, reads no input ([The tool list's size](#the-tool-lists-size)) |
| `kern hook <event> [--wait D] [--unanswered N]` | posts the host's hook JSON from stdin on `hook.<event>`, waiting up to 500 ms for a core whose start's claim stands; prints the answer as the host's hook JSON: a component's deny of the tool call with its reason, in the host's format, exit code 2 and the reason on stderr ([Agent tools](#agent-tools)); else the bound command's output, else the context the components told on `PostToolUse`, `UserPromptSubmit` and `SessionStart` (the [GitHub component](components/github/README.md)'s waiting tool calls) as `additionalContext`, nothing for a `pass` with nothing told; where no core answers, prints nothing; exits 0, or N where no command answered the event and nothing was told |
| `kern lint [--wait D] [--json]` | starts the core or attaches to it, as `mcp` does, asks the lsp component's `lsp lint` and prints every finding over the managed files, `path:line:col: rule: message`, and their count on stderr, or with `--json` the answer as JSON; exits 1 where there is a finding, 2 where no core runs the checker ([Language server](#language-server)) |
| `kern lsp [--grace D] [--idle D]` | the language server over stdio, which an editor starts: the checker's findings as diagnostics on every managed file; starts the core with the grace and idle given, or attaches to it, beside the protocol, and holds a session lease while the editor is connected ([Language server](#language-server)) |
| `kern forbids [--root DIR]` | scans the checkout's components (`components/`) against the forbid lists of the I/O components the binary carries and those declared there ([Forbid lists](#forbid-lists)): each use of another I/O module's entry is a line on stderr, naming the file, the line, the component's folder and the module that owns it, and the exit is 1; else one line on stdout and 0. It starts no core |

- **The standing file**, `.se/.runtime/standing.json`, readable by its owner alone: the bus's
  address, the pid with its start mark, the boot it runs in, the contract version and every bus
  user with its password. It reads as absent, and the next `serve` writes it anew, where it names
  an earlier boot, a pid that runs nowhere, or a pid now another process's.
  - The start mark is when the platform says the process started: Linux's `starttime` in
    `/proc/<pid>/stat`, macOS's `p_starttime`, Windows's creation time.
  - A pid whose mark differs, or whose bus accepts no connection, is another process's: a
    container restored from its snapshot hands out the last boot's pids, and may keep its boot id.
  - The core removes the claim once it has written the file.
- **The core's log**, `.se/.runtime/core.log`: each `serve` starts its lines with `kern: start
  <time>, boot <id>, pid <n>`, so a report of a core that ended quotes that core's run alone.
- **Leases.** Each `hook.<event>` with a `session_id` takes or renews that session's lease and
  `hook.SessionEnd` returns it; `call.core.lease`, `{"session_id": <id>, "end"?: true}`, does the
  same without a host's event, as the MCP door holds its session. A component beats on `lease.<i>` every 5 s; a placed instance's
  names read `stale` once three beats are missed.
- **Placements.** A wiring instance with `command = [...]` runs in a process of its own, started
  with `CORE_BUS`, `CORE_USER` and `CORE_TOKEN`; it commits on `commit.<i>` and the core checks
  each write against the component's declaration. A Go component placed out of process may
  declare no derivation or command; a declared component's process answers both.
- **Declared components.** A component no binary carries is declared by its `component.toml`
  alone, in `components/<name>/` or `.se/components/<name>/` of the checkout, one instance each.
  Its process attaches first, and the core runs its derivations, commands and verbs over the bus,
  as [A component in Python](#a-component-in-python) says.
- **The host's events.** An I/O component that declares `index.Hears` hears each `hook.<event>`
  with its payload before the core answers it; what it refuses goes to the core's log, and the
  hook passes. One that declares `index.Inlet` is handed its instance's commit at start, so it
  writes what comes in on its own. The [GitHub component](components/github/README.md) does both.
  - The [session record](components/session/README.md) hears every event and appends it to a
    file per session. It runs only where the checkout's ignored overlay `.se/wiring.toml` holds
    `[instances.session]` with `component = "session"`, after a restart of the core.
- **Time is an I/O module.** The [clock](components/clock/README.md) writes `time`, the time since
  its start, 10 times a second, and `second`, `minute` and `hour`, counters that count up. A
  component that acts on time reads the counter it needs (`index.Reacts`) and keeps its interval
  with a `clock.Relay` over its count, as the GitHub component's refresh does; no component keeps
  a timer of its own, and the clock's forbid list refuses a component that reads the system time
  itself ([Forbid lists](#forbid-lists)). Every wait for an answer measures its limit
  against the host's clock (`host.Clock`), which a test replaces with a clock it moves itself.
- **Guards.** An I/O component that declares `index.Guards` is asked after the event is heard:
  the first instance that refuses it answers the effect `deny` with its reason, and no bound
  command runs. The [files component](components/files/README.md) is kern's one guard
  ([Agent tools](#agent-tools)).
- **Hooks a command answers.** The wiring's `[hooks]` binds a host's event to a command, such as
  `Stop = "work_tickets.py stop-check"`; the core refuses to start where no instance declares it.
  The core runs it with one argument, `--hook=` and the event's JSON, and answers the effect
  `output` with the command's text, which `kern hook` prints as the hook's output. Where the
  command's process has not attached yet, or the command fails, the core answers `pass` with the
  reason at once, so the caller can answer the event another way (`--unanswered`).
- **The idle end.** The core ends 60 s after the last session's lease is returned (or after its
  start, where none took one), or after 30 minutes without a hook event or a call.

## The MCP door

`<name> mcp` (package `mcp/`) is an MCP server over stdio, built on the official Go SDK,
`github.com/modelcontextprotocol/go-sdk` (`go.mod` owns its version). A host spawns its configured
stdio servers as a session starts and drops one that is slow to answer, so the door never waits on
the core to answer its handshake and `tools/list`. It speaks both handshakes: `server/discover` of
the MCP spec of 2026-07-28, and the legacy `initialize` of a host that knows only an older one.
A host runs it as `<name> mcp --root <checkout>`, the binary on its PATH or reached through a
launcher, since its working directory need not be the checkout.

- **The catalog without the core.** `host.Catalog` reads what the core would serve: the binary's
  components, the checkout's wiring (`cli.Wiring`, its ad hoc overlay included) and the declared
  components' `component.toml`, and runs the catalog check. The tools come from it.
- **Start or attach.** Inside a checkout (a folder with `.git`) the door starts the core detached
  where none runs (`host.Start`, with its `--grace` and `--idle`), or attaches to the one that runs.
  Once the core runs it takes a session lease on `call.core.lease` and renews it every 30 s and on
  every call, each renewal an event; on EOF and on SIGTERM it returns the lease. A core that ended,
  idle or crashed, is started anew and the lease taken again.
- **The supervisor's step** is one pure function, `mcp.Next` (`mcp/step.go`): from the core's
  state, the lease, a start in flight and whether a call asked, it answers lease, start, fail or
  wait, with the span to wait; the supervisor only carries it out.
- **A call wakes the supervisor.** A call that finds no core running leaves the supervisor one
  signal, which never blocks the call and ends its wait, so a core that is gone is started at once,
  one start however many calls arrive. A core that ended at its start answers `failed` until Renew
  has passed: no signal starts it again before, so a wiring that cannot start is not spawned in a
  loop.
- **A call before the core stands** waits up to 5 s, then answers the typed error `starting`, its
  structured content `{"error": "starting", "message", "remedy"}`, while a start is in flight.
  The other kinds are `failed` (the core ended at its start, with what it said, or none runs and
  none starts), `no-checkout` and `command-failed`.
- **It never exits early.** Without a root folder or a path to its own binary it serves all the
  same, and every call answers `failed` with why; only EOF ends it.
- **Long answers come in pages** of at most 45,000 bytes (`mcp.PageLimit`), below a host's cut at
  about 50,000. Each page says how many there are; the tool's `page` argument asks the next, read
  from the kept answer, not run again.

| tool | what |
|---|---|
| `status` | the core's state and health, as `kern status --json`, and `core`, how the door came to it: `attached`, `started` or `started again` |
| `greeting` | the names and read-only commands the application names in `cli.Greeting`, read in order; a part the catalog lacks is left out, and with no part there is no greeting tool |
| `<instance>` | one per switched-on instance with read-only commands, named in snake_case (`mcp.ToolName`: `retro-due` reads `retro_due`): `command`, one of them, `args` and `page`; its description lists each with its `mcp` line and usage |
| `<instance>_write` | one per switched-on instance with writing commands, its arguments and description as `<instance>`'s: it runs one of them, which writes |

- **Read-only or writing, command by command.** A command's declaration says `read-only = true`
  where it changes nothing (`index.CommandDecl.ReadOnly`), whatever its class: an interactive
  command may still write, as one that syncs the checkout does. It says `writes = true` where it
  writes only through its requests (`index.CommandDecl.Writes`), never both. Only those two are
  tools: a command declared neither is in none.
  - A read-only tool carries `readOnlyHint`, `destructiveHint: false` and `idempotentHint`.
  - A writing tool says it writes: `readOnlyHint: false`, `destructiveHint: true` (MCP's reading
    of a writing tool that says no more, since the door cannot know a write only adds) and no
    `idempotentHint`.
  - `openWorldHint` is set on every tool that runs a component's command, which may reach an
    outside service. The hints neither grant nor force a host's approval.
- **A write goes through its I/O component.** A writing tool's call is the command's call,
  `call.<command>`, as a read-only tool's is: the core runs the command, commits what it writes
  and delivers each request it answers to the I/O verb the wiring binds its port to (`index`
  `Call`), so a write through the door passes the wire's type check and the verb's own, and
  leaves the record any call leaves. The door reaches no outside service itself.
  - An I/O component's own writing command asks its own verb the same way: a request port that
    names a verb of its component (`index.RequestPort.Self`) is bound to it where the wiring binds
    the port to nothing.
  - The [GitHub component](components/github/README.md) serves `github_write`: its commands
    `github issue-create` and `github issue-comment`, through its verbs of those names. They take a
    filled template, never free text, and check it against the template before anything reaches
    GitHub ([its README](components/github/README.md#writes-filled-templates-only)).
  - `components/github/door_test.go` files an issue through the door and the real core against
    the component's fake REST server.
- **The catalog check.** `mcp.Tools` refuses a read-only or writing command without its `mcp`
  line, when an agent uses it (`index.CommandDecl.MCP`; both declaration readers read every key),
  a writing command whose instance has no request port to write through, an instance whose tool
  name takes one of the door's own or another tool's, and a greeting entry not declared read-only.
  The door then serves the
  tools it made, and its status tool names the refusal; an application's test holds its own
  catalog to the check, as SE's `components/cmd/se/mcp_test.go` does.
- **Tests.** `mcp/door_test.go` drives the door with a fake client, JSON-RPC lines over pipes,
  against a fake core, and `mcp/step_test.go` holds `mcp.Next`'s table. The one contract test with the SDK's own client runs against the built
  binary and the real core: SE's `components/cmd/se/mcp_test.go`.

### The tool list's size

The tool list rides in every turn on every host, and a host may cut a long description without a
notice, so it is measured and can be held.

- **`<name> mcp --tools`** prints it: `mcp.Listed` asks the door's own server, with the SDK's
  client over an in-memory connection, so the list is the served one, in the SDK's order (by
  name), and two runs print the same bytes. A catalog the check refuses is said on stderr and its
  tools listed all the same; without the flag `mcp` serves as before.
- **`mcp.Measure`** (`mcp/measure.go`) is pure: tools in; their count, order, each description's
  bytes and characters, each tool's bytes as served and their sum out. `Surface.Over` holds it to
  an `mcp.Budget` of three numbers (most tools, most bytes for one description, most bytes for the
  list), each unset at 0; `Surface.Table` writes it as Markdown.
- **An application holds its own**: SE's `components/cmd/se/mcp_surface_test.go` compares its
  committed `testdata/tools.json` with the live list, naming the command that rewrites it, and
  logs the measure against its budget, `surfaceBudget`, which reports only while unset.

## A component in Python

`python/kern/` is the SDK, standard library only (SE's
`docs/go-core.md`, "How a component in another language attaches"). A
component is a folder: its `component.toml`, and an `__init__.py` that makes it with
`kern.component("<name>")` and decorates its functions; `contract/fixture/tally/` is the sample.
The wiring places it with `command = ["python", "-m", "kern", "run", "components/<name>"]`, the
SDK on `PYTHONPATH`.

| subject | the process | the core |
|---|---|---|
| `commit.<i>` | first `{"attach": {"contract": 1, "hash": "sha256:<hex>"}}`, then `{"writes": [...]}` | refuses another contract, another declaration, and a write before the attach |
| `run.<i>` | hears that an input moved: reads `in.<i>`, commits each derivation's value | sends it after each commit that moves an input, and on the attach |
| `in.<i>` | asks its inputs | answers them by local port, `config/<key>` among them, at the newest revision |
| `cmd.<i>.<command>` | answers one step: `text`, `writes`, `requests`, `then`, or `error`; its lines go out on `say.<i>.<call>`. A message without a reply, `{"call", "stop"}`, is the stop word: it drops that call's continuation and sets its `kern.abandoned()` | sends the first step with the arguments and inputs, each later one with the answers; the stop word once it gives up a call whose step runs or whose continuation waits (`docs/go-core.md` "A call given up") |
| `io.<i>.<verb>` | an I/O component answers `{"answer": ...}` or `{"error": ...}` to `{"call", "request"}` | sends a request bound to the verb; and, without a reply, `{"call", "stop"}` once it stops waiting for that request (SE's `docs/go-core.md` "A call given up") |
| `say.<i>.<call>` | says a step's line as it goes, `{"line"}`, and beats `{}` every 5 s while the step runs, on its own call's subject; where the bus refused the step's answer, says `{"refused"}` with the bus's reason | hands each line on to the call's caller; each word restarts the step's wait, `StepWait` (10 minutes, `KERN_STEP_WAIT` for a served core), so a step that beats runs as long as its process, its answer taken however late (`KERN_REPLY_WAIT` bounds it for a test); `{"refused"}` fails the step at once |
| `call.<command>` | | a client's call: `{"args": [...], "progress"?}`, answered with the lines, the text or the error; with a progress subject in the caller's inbox, each line goes there as it is said and a beat every 5 s (`docs/go-core.md` "Progress and long calls"); `call.core.abandon`, `{"call": <its reply subject>}`, gives up a call in the asker's own inbox; a call whose reply subject no connection hears any more, its caller killed, is given up the same way |

- **The declaration hash** is the SHA-256 of `component.toml`'s bytes, line ends read as LF,
  written `sha256:<hex>`: the bytes, not what they parse to, so Go and Python answer one hash.
- **Subject tokens.** A name's segments are one token each, and every byte of a segment, a command
  name or a verb but letters, digits, dashes and underscores rides percent-encoded:
  `tally note` rides as `tally%20note`, the family key `v1.2` as `v1%2E2`. `get.<name>` also reads
  the name from its body. `contract/subjects.json` holds Go and Python to it.
- **Types** are `int`, `float`, `string`, `bool`, `[T]` or a named JSON shape such as
  `github.issues`, which Go reads as `any`; a wire joins equal types and equal shapes.
- **A family read whole.** A Go input of type `map[string]T` bound to a family of `T` reads every
  key standing, `{}` where none does, and its derivation runs once for a change at any key; an input
  of type `T` reads the family at the key that changed, once per key. A command may read a family
  only whole.

## Files

kern holds named values in memory with one writer each, and with the files component it holds
files too, the warm model: an application names its managed folders in its settings (`folders`),
and the component holds each managed file as names in the index: its path, its content hash and,
below `content-limit`, its content. This is the person's decision of 22 Nov (deck "The warm model",
option B).

- **Reads and searches** over managed folders answer from the model, through its verbs `read`,
  `list` and `search`.
- **A write** through its verbs `write` and `remove` names the version it read and is refused,
  naming "re-read", where the file changed since.
- **A hand edit**, and a host without hooks, still write the disk past it, which is acceptable: a
  file watcher keeps the model in step, a hash reconcile at start and on the watcher's overflow
  catches what the watcher missed, and a cookie at start finds a deaf watcher out, after which
  reads read the disk.
- **Outside managed folders** nothing changes.

The component's [README](components/files/README.md) is its contract.

## Agent tools

The person's decision of 22 Nov (deck "Files, templates and agent tools", lean routing): kern
refuses only a write to a managed file, naming the way to do it, and answers a search over a
managed folder from the warm model; every other tool call passes, and with kern down every one
does. It does so through the hook every
host the person uses runs before a tool, never a path only one host has.

> Just understand that we're not only using Claude Code. We're also using Copilot, and the hooks
> need to. We can't use hooks of which we know that they only work in Claude Code in general.

### What is refused

Only a write to a managed file ([Files](#files)): the files component guards `PreToolUse`
(`index.Guards`, [its README](components/files/README.md#the-guard)) and denies a call that writes
one, naming the files and its tools `files` and `files_write` (`files read`, then `files edit`,
`files write` or `files remove`, each naming the version read). What a call writes is
`host.ToolCall.Writes`:

- **File tools:** Claude Code's Write, Edit, MultiEdit and NotebookEdit; the Copilot CLI's
  `create`, `edit`, `str_replace_editor` (but its `view`) and `apply_patch`, each mapped onto its
  Claude name (`host.CopilotTools`).
- **Shell commands** (Bash; the Copilot CLI's `bash` and `powershell`), parsed, never run or
  blocked whole (`host.ShellWrites`): output redirections, `tee`, `cp`, `mv`, `install`, `ln`,
  `rm`, `unlink`, `touch`, `truncate`, `shred`, `dd of=`, `sed -i`, `perl -i` and PowerShell's
  content and item cmdlets, each target resolved against the call's working directory and any `cd`
  before it. A target the parser cannot be sure of, a variable, a glob or a command substitution,
  is no write, and a script that writes from inside (`python -c`) is not seen: the lean routing
  refuses only what it knows, and the watcher and compare-and-swap keep the model right past it.
- **Everything else passes:** reads, files outside the managed folders, kern's own folder `.se/`,
  every event but `PreToolUse`, and every call while no files instance runs.

### Searches answered from the model

A Grep or a Glob over a managed folder (Claude Code's, the Copilot CLI's `grep`, `rg` and `glob`)
is answered from the warm model as the deny's reason, which says it is the search's result and not
an error, so the native tool does not run (`files` `answerSearch`, [its
README](components/files/README.md#the-guard)):

- **Grep** answers, as the native tool does, the files that match (`Found N files` and their
  paths, its default), their lines (`output_mode` `content`, `path:line:text`) or their counts
  (`count`), with `-i`, `glob` and `head_limit`; paths are absolute and sorted by path, where the
  native tool sorts by time.
- **Glob** answers the managed files the pattern matches, `**` any number of folders; literal
  folders at the pattern's start move its base, so `docs/**/*.md` from the root is answered.
- **On any doubt it passes**, and the native tool reads the disk: the model is not watched (the
  cookie was not heard), the path reaches past the managed folders (a Grep of the whole checkout),
  an argument the model does not answer (context lines, `type`, `multiline`, `offset`, any other),
  a pattern Go's `regexp` does not take or a glob with braces, a file the Grep would read that the
  model holds by hash alone (larger than `content-limit`; a binary one is skipped, as ripgrep
  does), or more than 1000 answers.
- The model holds every managed file but `.git`, `node_modules` and its own, hidden ones
  included, and reads no `.gitignore`: within a managed folder the answer may name a file
  ripgrep would skip.

### How each host receives the deny

`kern hook PreToolUse` (or `preToolUse`) reads the host's payload through `host.ParseToolCall`,
either field style, and prints a deny in the shape that payload's host reads
(`cli.DenyOutput`), with exit code 2 and the reason on stderr (`cli.DenyExit`):

| host | registers | payload | the deny it reads |
|---|---|---|---|
| Claude Code | `PreToolUse` | `tool_name`, `tool_input` | `hookSpecificOutput.permissionDecision` `deny` and `permissionDecisionReason`, which the agent reads; exit code 2 blocks and takes that reason |
| Copilot CLI, VS Code compatible format | `PreToolUse` (PascalCase), as SE's `.github/hooks/se.json` does | `tool_name` as the Claude name (`bash` reads `Bash`), `tool_input` | `hookSpecificOutput`, as VS Code's; exit code 2 denies the call in the CLI whatever JSON it reads |
| Copilot CLI, camelCase format | `preToolUse` | `toolName` as the runtime name (`create`, `edit`, `bash`, `grep`, `glob`), `toolArgs`, an object or a JSON string | `permissionDecision` `deny` and `permissionDecisionReason` at the top level; exit code 2 denies |
| VS Code (Copilot harness) | `PreToolUse` | `tool_name`, `tool_input` | `hookSpecificOutput`; exit code 2 hands the model the standard error, the reason |

### Which hosts have such a hook

The comment of 22 Nov on kern#21 asks which hosts have a hook that can refuse a tool call; where
one has none, its writes go past the warm model, which the person accepted: the watcher and
compare-and-swap keep the model right.

> So a host without hooks or any other tool still writes past it. Yeah, that's acceptable.

| host | its hook | the gap |
|---|---|---|
| Claude Code (terminal, IDE, cloud) | `PreToolUse` | none known |
| Copilot CLI | `preToolUse` / `PreToolUse`, from user, project and plugin hook files | a command hook that crashes fails closed (`kern hook` exits 0 on every error, 2 only on a deny); which reason the CLI shows for exit code 2 with JSON on stdout its reference does not say, so the reason is on both |
| Copilot cloud agent (github.com) | the repository's hook files, `bash` entries alone; `preToolUse` fires | the runner has no kern binary unless the repository's setup steps place one; without it the hook prints nothing and every call passes |
| VS Code, Copilot harness | `PreToolUse`, the CLI's runtime and Claude names | none known |
| VS Code, Local harness (deprecated, removed February 2027) | `PreToolUse` | its tool names differ and are not mapped (`host.CopilotTools` holds the CLI's), so its writes and searches pass |
| any MCP host without a hook that refuses a call | none | its native writes pass; the MCP door's `files` and `files_write` are the same way in, served to every host that runs the door |

Registering the hook is the application's: SE's Claude Code plugin runs `kern hook` on
`PreToolUse` through its `se_hook.sh`; its Copilot hook files register `SessionStart` and `Stop`
alone, so a Copilot session under SE sees no deny until they register `PreToolUse` too.

Every host fails open on a hook's time-out, and `kern hook` prints nothing and exits 0 where no
core answers: with kern down every tool call passes, reads and searches included. Sources: the
Copilot CLI's hooks reference (docs.github.com, copilot/reference/hooks-reference), VS Code's
(code.visualstudio.com, docs/agents/reference/hooks-reference) and Claude Code's
(code.claude.com, docs/en/hooks), each read 2026-11-22.

## Language server

Checks on managed files are served through the Language Server Protocol, the standard every editor
speaks, and the language server uses the same checker as the tests: opening the files in an editor
shows exactly what the tests would find. This is the person's decision of 22 Nov, after quackitect
v5's `lsp` I/O module, whose one checker (`Sweep`) the editor's panel, the command line's `lint`
and the battery all asked, so there was no second road.

- **One checker.** Package `lint` holds the rules: each managed file's template or schema, links
  that reach nothing, and names over their caps. The [lsp component](components/lsp/README.md)
  runs it over the warm model ([Files](#files)), and its command `lsp lint` answers the findings.
  Three roads ask it: the language server, `<name> lint`, and the tests through
  `lsptest.Findings(root)`, which runs the checkout's files and lsp instances on an index of their
  own. `components/lsp/lsp_test.go` holds the three to the same findings over one checkout.
- **The server.** `<name> lsp` speaks the protocol over stdio. It answers the handshake at once
  and starts the core, or attaches to it, beside the protocol, as the MCP door does, holding a
  session lease while the editor is connected. It publishes the findings as diagnostics (an error
  each, its rule as its code, `kern` as its source) on every managed file, not only the open ones,
  and an empty list on a file whose findings are gone.
- **Its redraw roads**, v5's: an open and each typed change check the editor's buffer in place of
  the file (full sync); a save, a close and a change on the disk check the model, which the files
  module keeps in step with the disk, and the server hears the lsp instance's `findings` change
  on the bus (`val.<name>`).
- **Beside it**, standard servers stay for what they do well: prose (`vale-ls`) and code
  formatting (Biome). The editor runs them next to `<name> lsp`.

### In VS Code

VS Code starts a language server through an extension of its own, which names the command and the
documents it serves. The editor runs `<name> lsp` in the folder it opened, the checkout, so the
server finds the checkout as every verb does; `--root DIR` names another. A minimal extension is two
files in a folder of their own, with `vscode-languageclient` installed beside them (`npm install
vscode-languageclient`):

`package.json`:

```json
{
  "name": "kern-lsp",
  "version": "0.0.1",
  "engines": { "vscode": "^1.82.0" },
  "main": "extension.js",
  "activationEvents": ["workspaceContains:wiring.toml"],
  "dependencies": { "vscode-languageclient": "^9.0.1" }
}
```

`extension.js`:

```js
const { LanguageClient } = require("vscode-languageclient/node");

let client;

exports.activate = () => {
  // kern's own binary is `kern`; an application names its own, such as `se`.
  client = new LanguageClient("kern", "kern",
    { command: "kern", args: ["lsp"] },
    { documentSelector: [{ scheme: "file", language: "markdown" }, { scheme: "file", language: "json" }] });
  return client.start();
};

exports.deactivate = () => client && client.stop();
```

Install it with `code --install-extension` on the folder packed by `npx @vscode/vsce package`, or
open the folder in VS Code and run it with F5 (an Extension Development Host). The findings show in
the editor and in the Problems panel, every managed file's, the closed ones too; the open Markdown
and JSON buffers are checked as typed. The server's own log is the extension's output channel,
`kern`. Another editor starts it the same way, with the command `<name> lsp` over stdio, such as
Neovim's `vim.lsp.start({ name = "kern", cmd = { "kern", "lsp" }, root_dir = vim.fs.root(0, ".git") })`.

## Tests

```sh
go vet ./... && go test ./...
```

kern's `se.toml` declares its tests, with their time and budget (SE's README "Test budget"):
`[tests]` is kern's Go tests, and beside them `[tests.beside.python]` the Python SDK's tests
(`python/tests`, pytest through uv from the root's `pyproject.toml`), `[tests.beside.template]`
the template's, and `[tests.beside.release-guard]` (below).
`work_tickets.py suite --worktree .`, SE's suite command at kern's SE pin, runs them all, and
`integrate` runs the same, so a red test refuses the integration.

No test waits on the wall clock. A time limit under test (a step wait, a reply's expiry, a hang-up,
a beat) is shortened to milliseconds through the core's options or its `KERN_*` variables, or the
case reads a fake clock or the state that settles it instead; a deadline only bounds a wait for
something that happens at once where the test passes.

`.github/workflows/check.yml` is the check `main`'s ruleset requires, `check`. Its job `check`
gates the commits with SE's public check, holds a pull request to the work model's issue rule and
its field report, fails where `gofmt -l` lists a file, vets kern and runs the suite (`--full`
nightly), all with SE's tools at the commit `se.lock` locks for `[modules.se]`. The job
`kern-platforms` runs kern's vet and tests on Windows, where the detached start and the stop
differ; `check` needs it and fails unless every leg passed. `kern-platforms-queue` reports a leg
that never got a runner, and `check-rerun.yml` re-runs such a first attempt once (SE's README
"Platform legs and their queue"). kern is built for darwin, and not tested there: no leg runs on
macOS.

The contract suite runs `contract/cases.json` three times: against the Go fake
(`contract/fake_test.go`), against the real core with tally's Python process placed and attached
(`contract/real_test.go`, Python 3.11 or later, from `KERN_PYTHON` or the path), and against the
Python fake (`python/tests/`, `se.toml` `[tests.beside.python]`, which needs no Go).

The long-step case, `contract/longstep_test.go`, asks a relay step after a local phase longer than
the server's expiry of a reply permission, NATS's two minutes, the one limit on the path that no
fake clock reaches. It runs in `go test ./...` in under a second: `KERN_REPLY_DEFAULT` (`cli`)
shortens that expiry to 150 ms and the phase works 0.3 s, so the answer the bus refused on 14 Nov
is refused again with the fix reverted. Its fast guards, in milliseconds: `bus/expiry_test.go` holds
that an instance's reply permission never expires where the core sets no reply wait, and
`host/refused_test.go` that a step whose answer the bus refused fails at once.

## Expectations

A component's tests never fake the component on the other side of a name. Each component keeps
beside its tests one expectation file, stating what its tests rely on of each name it reads and
each request it sends, and the names and requests it claims. The expectations pool by name, never
by component, and the components one wiring loads claim and meet them. This is the person's
decision of 22 Nov, after Pact's consumer expectations and provider states, without Pact's
contracts between named pairs.

```toml
[[expect]]                   # a value it reads
name = "numbers"             # the name it pools under
port = "numbers"             # its own input port, which the fake seeds
given = "numbers stand"      # optional: the precondition its claimer sets up
value = [1, 2, 3]            # the least it relies on

[[expect]]                   # a request it sends
name = "lines.append"
request = "post"             # its own request port, which the fake answers
ask = ""                     # what it sends
refused = true               # what it relies on: refused, or answer = <value>
exact = ["ask"]              # the paths matched by equality; match = "exact" is all of them

[[claim]]                    # a name it writes, on its output port
name = "numbers"
port = "numbers"

[[claim]]                    # a request it answers, with its verb
name = "lines.append"
verb = "append"
```

- **Its name** is lowercase words joined by dots, such as `github.issues`. It is the only thing a
  reader and its claimer share. A file is `expect.toml` in a component's folder, or
  `<component>.expect.toml` where a folder holds several components.
- **Matched by type.** A value or an answer meets its expectation where it holds the same kinds: a
  whole number for an integer, any number for a float, a string, a bool, a list with at least one
  item where the expectation's has one (each item like one of the expectation's), and a table with
  at least the expectation's keys. The paths `exact` names (`value.number`, `answer`,
  `value.labels[]` for each item) are matched by equality, so an expectation states only what
  would break its reader if it changed.
- **Declare: the reader's test.** `kerntest.Expecting(c, file)` and `kern.fake.expecting(c, file)`
  answer a component's tests from its own file. Each input port reads its expectation's value, and
  each request gets the answer or the refusal of the first expectation whose ask it matches. An
  expectation that holds its ask exact goes first, then one matched by type. Within each, one of
  the givens `Given` names goes before one with none. Such a fake refuses `Seed` and `Answer`, and
  a request no expectation answers fails the test.
- **Claim: the writer's test.** `kerntest.ReadPool(files...)` pools the readers' files by name, and
  `kerntest.Module{Component, File, Given}.Meet(pool)` holds the component to every expectation
  pooled under the names it claims, each in a fake of its own. It runs the setup its test gives for
  that expectation's given, then reads the claimed output (derived where it is a derivation) or
  serves the ask on the claimed verb. `kern.pool.Module(...).meet(pool)` is the same in Python.
- **The completeness check**, `kerntest.Check(wiring, modules...)` and `kern.pool.check`, runs over
  the components a wiring's switched-on instances load. Every expectation of a loaded component is
  claimed by exactly one loaded component and met by it, else it fails with `unclaimed`, `claimed
  twice` or `unmet`, naming the name, the expectation and the reader's file. Two components that
  claim one name, two doors to two trackers, both pass where no wiring loads both. The check runs
  the claimers' own setups and never answers for a component itself. A component declared by its
  `component.toml` alone takes part as a reader. As a claimer it is met by its own language's
  tests, so a Go check cannot run it.
- **The worked example**, `index/fixture/expect_test.go`. tally (`tally.expect.toml`) and the clerk
  (`clerk.expect.toml`) are answered from their files. counter claims `numbers` and the ledger
  claims `lines.append`, and each meets the pool of the Go fixtures and of the sample Python
  component (`contract/fixture/tally/expect.toml`, tested in `python/tests/test_kern_sample.py`).
  A wiring that loads them is complete, and one without counter leaves `numbers` unclaimed.
  `kerntest/testdata/expect-cases.json` holds the Go and Python readers to the same refusals and
  matches.

## Hot tests

An I/O module reaches the outside world, and its fake answers from memory. Its hot test, its run
against the real service, is the only proof that the fake tells the truth. So the hot test meets
every expectation pooled under the names the module claims against the real service, and records
each real answer beside the expectation it met. The readers' fakes then answer with those
recordings. This is the person's decision of 22 Nov (deck "Who provides the fakes", option B):
hand-written answers for outside services were not picked, since nothing checks them against the
world.

- **One command runs it.** `KERN_HOT=1 go test -count=1 -run '^TestHot$' ./components/github/`
  is the GitHub component's. `kerntest.Hot{Name, Module, Ask, Answer}.Record(pool)` meets the pool
  of every expectation file in the checkout (`kerntest.ExpectationFiles`). The module's `Start`
  sets each fresh fake up against the real service and starts it (`Fake.Start` runs its inlet),
  and `Fresh` makes it a component of its own each time. Without `KERN_HOT` the test is skipped,
  and the normal suite meets the same pool against the module's fake service
  (`Module.Meet`, `kerntest.Check`).
- **A recording** stands beside its reader's expectation file, `<file>.recorded.json` beside
  `<file>.toml`. Each names its expectation (its place, name, port or request, given and ask), its
  hot test and the time it was recorded. Its value or answer is the real one, trimmed to the keys
  and items the expectation states. So it holds what the reader relies on and nothing else of the
  service, and no recording names kern's home (the GitHub hot test also writes the home back as
  the placeholders `owner` and `repo`).
- **The reader's fake answers from it.** `kerntest.Expecting` and `kern.fake.expecting` answer an
  expectation with its recording where one exists, else with what the file states. `Stale()`
  (`stale` in Python) names each recording older than `kerntest.RecordingAge`, 30 days, or the age
  the test sets (`MaxAge`, `max_age`). Such a recording still answers.
- **A recording that no longer fits its expectation fails.** One whose value or answer no longer
  meets its expectation, or that is of another name, port, request, given or ask, or of an
  expectation its file no longer holds, is refused as `misfit` by the reader's fake and by the
  completeness check, naming the hot test to run again. A hot run that fails keeps the earlier
  recordings of what it did not meet, to age, and drops those that no longer fit.
  `kerntest/testdata/record-cases.json` holds the Go and Python readers to the same refusals and
  stale lines.
- **The order.** The hot tests run first and the readers' tests read what they recorded:
  `.github/workflows/hot.yml` runs each I/O module's hot test, then the complete Go suite. It runs
  on every pull request and push to `main` that touches an I/O module, and nightly. It is no
  required check, so its failure blocks no unrelated work: it opens an issue, `hot test failed`,
  or comments on the one still open. A change to an I/O module runs its hot test and commits the
  recordings it writes.
- **The worked example.** The GitHub component claims `github.issues` and its read verbs
  (`components/github/expect.toml`, `hot_test.go`). The watcher fixture
  (`index/fixture/watcher.expect.toml`) expects `github.issue` and `github.issues`, and its test
  is answered from the real recordings beside it.

## Pending expectations

A reader's new expectation is an input to its reader, never an order to its claimer: it is pending
until its claimer first meets it, and while it is pending it blocks only its reader. This is the
person's decision of 22 Nov (deck "Who provides the fakes", option B), after Pact's pending pacts.

- **No flag.** Nothing stores that an expectation is pending. The reader's check refuses to land a
  change whose expectation its claimer does not meet, so every expectation the integration branch
  holds is one its claimer met when it landed. An expectation is **new** where the reader's file
  at the base holds none with the same terms (its place in the file does not count), and
  **pending** where it is new and its claimer's test, run now, does not meet it.
- **The base** is the git revision `KERN_EXPECT_BASE` names, such as `origin/main`, read with real
  git by `kerntest.ReadPool` and `kerntest.Check`: the variable is the choice the git door asks
  of a test binary, and the read only shows files. Unset, there is no base and every expectation
  counts as landed, as on the integration branch itself. A file no repository holds counts as
  landed; a file the base lacks is new throughout. `kerntest.ReadPoolSince(base, files...)` and
  `kerntest.CheckSince(base, wiring, modules...)` take the base as a `kerntest.Base`, and
  `kerntest.GitBase(backend, ref)` is the one `KERN_EXPECT_BASE` names.
- **The claimer's test** stays green: `Module.Meet(pool)` fails only on landed expectations, as
  `unmet`, and `Module.MeetPending(pool)` also answers the pending ones, each a line `pending: name
  "numbers": expect 6 (port numbers, given "four numbers stand") of tally.expect.toml waits on
  counter, which does not meet it yet: <why>`, which the test logs.
- **The reader's check** refuses the change with the same line, naming the claimer that must meet
  it. Once the claimer meets it, the two land and the expectation is an ordinary one.
- **Hot tests and recordings** ([Hot tests](#hot-tests)). A hot run (`kerntest.Hot.Meet`,
  `Record`) is a claimer's test too: a pending expectation fails it not, it records nothing of it,
  and it appends the pending line to `KERN_PENDING`'s file; once met and landed, the hot run
  records it like any other. A recording is checked against the reader's file as it stands,
  pending or not, so the completeness check refuses a `misfit` beside a pending expectation as it
  does elsewhere, and a reader's fake answers a pending expectation from its file until a hot run
  records it.
- **In Python**, `kern.pool.read_pool(files..., base=...)`, `Module(...).meet(pool)`, which answers
  the pending ones, and `kern.pool.check(wiring, modules..., base=...)` are the same, with
  `kern.pool.git_base(ref)` the base `KERN_EXPECT_BASE` names.
- **The requests.** `go run ./tools/expect pending [--base origin/main]` runs kern's tests against
  the base, each pending line appended to the file `KERN_PENDING` names, and lists the pending
  expectations per claimer, so an integrator turns them into requests for that module. Its design
  decides on each: it meets it, or declines it, a judgment call that climbs as an escalation.

## Forbid lists

Only an I/O module reaches its outside thing, and every I/O module declares a forbid list of what
it replaces: the calls, imports, commands and hosts only its own code may use. Any other
component that uses an entry is refused, mechanically, before the code is committed. This is the
person's decision of 22 Nov: a component that reaches past its I/O module is hidden from the index
and cannot be faked in tests. Prior art: Go's `depguard` and Python's `import-linter`.

```toml
[forbids]                                    # in component.toml, beside io = true
calls = ["time.Now", "time.sleep"]           # Go's <import path>.<name>, Python's <module>.<name>
imports = ["github.com/google/go-github"]    # a package, and every one below it
commands = ["gh"]                            # a program run by name, named in a string
hosts = ["api.github.com"]                   # a host named in a string
on = "folders"                               # optional: forbidden only on the folders this config key names
```

- **Declared.** A Go component declares it with `index.Forbids(index.Forbidden{...})`, a
  declared one with `[forbids]` in its `component.toml`; both readers refuse one on a component
  without the I/O flag, an entry of no kind's form, an entry named twice and a list that names
  nothing (`contract/declarations.json`). The clock forbids reading and waiting on the system
  time ([its README](components/clock/README.md)), the GitHub component `gh`, the REST API's host
  and the Go clients of GitHub ([its README](components/github/README.md)), and the files module
  direct file writes on the folders it manages ([its README](components/files/README.md)).
- **A list on some folders.** A list whose `on` names a config key of its module (a list of
  strings, the files module's `folders`) forbids its entries only on the folders that key names
  in the checkout's wiring (`wiring.toml` and its overlay), and its built-in value. Another
  component may use one where it declares the folders it writes, `index.Writes(...)` in Go or
  `writes = [...]` at the top of its `component.toml`, an I/O component's alone, and none of them
  is, holds or lies in a managed folder; one that declares none is refused, since it may write
  one. The session record declares `.se/.runtime/sessions`, so its writes pass until a wiring
  manages that folder.
- **The scan** (`forbid/`) reads the Go and Python files under `components/`, but tests
  (`*_test.go`, `test_*.py`, `*_test.py`, `conftest.py`) and the folders `testdata`, `tests` and
  hidden ones. In Go it reads each import, each selector of an imported package under any import
  name, and each string literal; in Python each import, each dotted name with the names an import
  binds read as what they stand for (`from datetime import datetime as dt` makes `dt.now`
  `datetime.datetime.now`, which `datetime.now` names), and each string. A comment names nothing.
  A method of a value, such as `p.write_text` on a path in a variable, is no dotted name of an
  import, so only `Path.write_text` itself is caught.
  A command is used by a string that is its name or a command line starting with it, a host by a
  string that holds it between characters no host name holds.
- **Whose code may use an entry.** The owner's own folder: a Go component's is the folder of the
  package that declared it (`index.Component.Package`), where that lies in the checkout's Go
  module; a declared one's is the folder of its `component.toml`. A kern component that an
  application's binary carries has no folder in the application's checkout, so no code there may
  use its entries.
- **When it runs.** Before code is committed, never at start: the catalog check does not scan, as
  that is too late and costs every start. `kern forbids` (an application's `<name> forbids`) is
  the command, and `cmd/kern/forbids_test.go` holds kern's own tree to it in `go test ./...`, so
  the landing's suite and CI's `check` refuse a change that breaks it. SE's commit hook runs no
  command of a project's yet, so a commit is held to it only once SE's hook runs one.
- **The core** (`host/`, `bus/`, `cli/`, `mcp/`) is no module and is not scanned: its waits for an
  answer read `host.Clock`, and the timers check (`kerntest/timers_test.go`) holds every file of
  kern to starting no timer outside the clock component and the host's clock.

## Release

`release.toml` owns kern's version and where its release is published, a path under the home the
checkout's origin names ([home](home/home.go)), so it names no host, owner or repository:

```toml
version = "0.14.0"
download = "releases/download/v{version}"
binary = "kern"
package = "./cmd/kern"
```

- **The assets:** `<binary>-<version>-<os>-<arch>.tar.gz` for `linux` and `darwin`, `.zip` for
  `windows`, each on `amd64` and `arm64`, each holding the one binary (`<binary>.exe` on windows),
  and `SHA256SUMS` over the six, which `sha256sum -c SHA256SUMS` checks. `go run ./tools/release
  build --dir <folder> --out <dir>` builds them, the same bytes for the same commit. The binary is
  `kern` from `./cmd/kern` by default; an application names its own with `binary` and `package` in
  its `release.toml`, and the tool builds its folder (`--dir`).
- **A binary input changes with its version:** `go run ./tools/release guard --dir <folder>`
  refuses where the tag the folder's `release.toml` names is cut already and a file its binary is
  built from (each imported package's files but tests and prose, embedded files, the modules'
  `go.mod` and `go.sum`) differs since that tag; it names the files and the `release.toml` to
  raise. A tag not cut yet passes: the push to `main` that carries the version cuts it. kern's
  suite runs it for the bare core beside its tests, `se.toml` `[tests.beside.release-guard]`. On
  a work branch `$SE_COMPONENT_KINDS` (`se=patch,kern=minor`) names the kinds its field report states, and a
  changed binary of a stated kind passes: SE's landing assigns its version (SE's README
  "Component versions").
- **The workflow:** `.github/workflows/release.yml` builds the six for the bare core on every pull
  request and push to `main`, checks them with `sha256sum -c`, and runs [the template](#the-template)'s
  tests. On `main` it publishes the release `v<version>` under kern's own releases, where it does
  not exist yet, so the push that changes `version` cuts it; a release that exists is never
  changed. A rerun is `workflow_dispatch`. The release tool reads the tag from `download` and the
  repository from the checkout's origin, and refuses to publish into another repository than the
  one it runs in (`plan --repository`).
- **An application's release:** an application builds its own binary through the reusable
  `kern-release.yml`, the copy SE writes into it (SE's `scripts/se_workflows.py`, which keeps only
  `workflow_call`), called with its binary's folder:

```yaml
  jobs:
    release:
      uses: ./.github/workflows/kern-release.yml
      with:
        directory: <the application's binary folder>
```

- **Fetching it:** `<download>/kern-<version>-<os>-<arch>.tar.gz` and `<download>/SHA256SUMS`, the
  download under the home origin names, the archive checked against its line, then unpacked.
  Where origin names no home, nothing is fetched: the start builds the binary from its folder.

| asset | measured on 13 Nov in a Claude Code cloud container, 4 cores, Linux amd64, 5 runs |
|---|---|
| `kern-0.1.0-linux-amd64.tar.gz`, 6.44 MB (16.0 MB unpacked), built by `tools/release` and served from loopback: download, `SHA256SUMS`, `sha256sum -c`, `tar -xzf`, first run | 157 to 176 ms |
| nats-server v2.11.9 linux-amd64, 6.35 MB, from its GitHub release through the container's proxy: the same steps | 0.68 to 1.27 s |
| building the six, `go run ./tools/release build`, warm module cache | 17.5 to 18.7 s each, 110 s in all |

## The battery

`kern-battery.yml` is the reusable CI every kern application calls. SE ships it to each
application, as the battery runs SE's `integrator-pass` action: the application calls the copy SE
writes into it (SE's `scripts/se_workflows.py`):

```yaml
jobs:
  check:
    uses: ./.github/workflows/kern-battery.yml
```

It sets up the Go of the application's `go.mod` and uv, vets the Go code, and runs the
application's `se.toml` `[tests]` command, its Go tests and its Python tests, held to that table's
one budget: over `target_seconds` it warns, over `limit_seconds` or on a failed test it fails, as
SE's own suite (SE's README "Test budget"). Its input `directory` names the application's
folder, `.` by default.

Where the application's `se.toml` sets `trust_integrator`, the battery's job `trust` passes it on
the integrator's record of exactly its tree, and the vet and tests do not run; the template
keeps it off (SE's README "Trusted landings").

## The template

`template/` is the bare application ([its README](template/README.md)): the core with no
components, an empty `wiring.toml`, `components/`, `types/`, `se.toml` with its `[tests]` budget
and `[modules.kern]`, an `AGENTS.md`, a battery that starts the bare core, and a `check.yml` that
calls the battery. It is a Go module of its own, so kern's `go test ./...` leaves it out; the
release workflow runs its tests on every change to kern.

## The catalog check's budget

The start reads `wiring.toml` and checks the whole catalog before anything runs. The budget is
`CheckBudget` in `index/budget_test.go`, which a test holds the median of nine checks of a
200-instance catalog to; `go test -run XXX -bench . ./index/` measures it.

| catalog | measured on 13 Nov, 4 cores, Go 1.24.7, 5 runs |
|---|---|
| 20 instances | 0.16 to 0.20 ms |
| 200 instances | 1.54 to 1.74 ms |

## The start

From the core's process start until a client's first request is answered. Measured on 13 Nov in a
Claude Code cloud container, 4 cores, Linux amd64, Go 1.24.7, 5 runs each, the binary built warm
with `go build -o kern ./cmd/kern`:

| what | command | wall time |
|---|---|---|
| process start to the standing file, then a client's `get.core.health` answered | a probe that starts `kern serve --root <tmp>` and reads the standing file every 100 µs, then dials as `client` and asks | standing file 9.8 to 13.0 ms, answered 11.0 to 14.5 ms |
| the same with v5's one wait for the bus, `ReadyForConnections(10s)` | the probe against a build with that wait | standing file 33.4 to 36.5 ms, answered 34.4 to 38.0 ms |
| a hook's view: `kern start` returns, then `kern status --wait 5s` says running | `kern start --root $R; kern status --root $R --wait 5s`, timed with `date +%s%N` | start returns in 6.5 to 8.7 ms, running 15.8 to 20.8 ms after the start began |

The server's own wait sleeps 25 ms between looks at its listener for any wait above 25 ms, so the
bus looks in waits of 20 ms (`bus/bus.go`, `readyLook`), and stands as soon as it listens.
