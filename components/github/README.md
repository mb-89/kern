# github

kern's GitHub component: the one I/O component that reaches GitHub. Every other component reads
GitHub through its verbs, so no other component holds a token or a client. It names no
application. Its design is SE's `docs/go-core.md`, "GitHub in the
cloud".

## The backends

The config key `backend` chooses at start, and the output `backend` names the choice:

| backend | where | how a read is answered |
|---|---|---|
| `token` | a token is found: `GH_TOKEN`, else `GITHUB_TOKEN`, else `gh auth token` | GitHub's REST API, in process |
| `relay` | none is found, or in a cloud session | the session's own GitHub tool, its answer handed back by the PostToolUse hook |

`auto`, the default, takes the relay in a Claude Code cloud session (`CLAUDE_CODE_REMOTE=true`),
whose `GH_TOKEN` is the proxy's stand-in that code cannot use; elsewhere the token where one is
found, else the relay. `token` without one refuses the start.

- **The token backend** keeps each path's ETag and answer, so a read of an unchanged one is
  answered `304` and costs no quota. A list read is paged, 100 items a page, and each page keeps
  its own ETag, so an unchanged page costs no quota either. Every `refresh` (2 minutes) it reads
  each snapshot and list it holds again, each page with its ETag. It keeps no timer: its input
  `second` reads the [clock](../clock/README.md)'s `clock/second`, a count of seconds, and on each
  count a `clock.Relay` returns at once until `refresh`, in whole seconds, has passed in counts
  since the last refresh.
  The refresh then runs on its own, so a slow GitHub never holds the clock, and a second that
  comes while one still runs starts none. A wiring that leaves `second` built-in refreshes
  nothing.
- **Its waits.** Every wait, `relay-wait`, `step-wait`, `tell-wait`, the pacing below and each REST
  request's limit (`RequestWait`, 30 s, its body read included), measures
  against the host's clock (`host.Clock`), `Options.Clock`, so a test drives each with a clock it
  moves and never waits.
- **Its pacing.** A `429`, or a `403` with `Retry-After` or `X-RateLimit-Remaining: 0`, is a rate
  limit. The backend waits what `Retry-After` asks, else until the quota's reset, else a minute,
  and every read waits with it, a later page of a list among them. It sends a page four times at
  most, and fails one whose wait is over `pace-max`. A `403` that is no rate limit fails at once.
- **The relay** answers a snapshot or list younger than `refresh` at once. Otherwise the read goes
  out as an entry of the family `outbox`: the tool's bare name and its input, and when it was
  asked. It waits up to `relay-wait` (`0s`) for the answer, else fails naming the tool call to
  run. A paged read's entry names the next page the session is to run, with the cursor or page the
  last answer gave; the entry stands until a PostToolUse answers it.
- **Every GitHub tool answer lands.** The component hears each PostToolUse the core is handed.
  - A single read: `<server>__issue_read` (method `get`) or `<server>__pull_request_read` (methods
    `get` and `get_check_runs`), with an owner, a repository and a number, lands in its snapshot.
  - A comment read: either tool's method `get_comments`, with an owner, a repository and a number,
    lands in `comments`, page by page.
  - A list read: `<server>__list_issues` (with a `state` and `labels`) or
    `<server>__list_pull_requests` (with a `state`), with an owner and a repository and nothing
    that narrows or reshapes the list further (`fields`, `since`, a sort, `head`, `base`), lands
    in `issue-lists` or `pull-lists`, page by page. One that names such a narrowing is another
    list than a name holds, so it passes unread.
  - Each lands asked or not, and drops its outbox entry; a paged answer lands its name once its
    last page is heard, and a first page nobody asked with more behind it lands nothing, since it
    is no whole list. Other tools and methods pass unread; an error answer is logged.

## The verb `step`: a write tool through the relay

The reads above are the tools the component knows; the verb `step` runs any GitHub tool through
the relay, so a command's write steps (open a pull request, merge, comment, close) reach GitHub
through the session the same way. It asks `github.step`, `{"tool", "input", "note"?, "match"?,
"wait"?}`: the tool's bare name (such as `merge_pull_request`), its input, the note the agent
watches for, the input keys an answer must carry equal, and how long to wait, up to `step-wait`
(`60m`).

- Without `match`, every input key whose value is no text over 200 bytes must match, so a long
  body the session re-wraps is read back from the answer; owner and repo match regardless of case.
- The step stands in `outbox` until a PostToolUse of that tool with that input answers it. The
  verb answers `github.step-answer`, `{"tool", "input", "read", "data"}`: the input the session
  sent and the tool's answer, as JSON where it is JSON, else its text.
- A tool use two waiting steps match answers the one asked first.
- A step whose note opens with `step <n>` (`se_relay.py` `numbered`) is kept by its number once
  answered. A PostToolUse that matches no waiting step but such an answered one answers nothing;
  its hook tells the one line `step <n> is answered: the current step is <m>` first, `m` the
  oldest numbered step waiting, then what waits.
- A step whose call is given up leaves `outbox` at once, so the agent is never told a step that
  no one waits for (`docs/go-core.md` "A call given up").
- Only the relay runs steps; with a token a command runs its GitHub steps itself.

## Writes: filled templates only

The component files an issue and comments on one, and only from a filled template: a write names
a template and its fields, never a title or a body. It checks the fields against the template
before anything reaches GitHub, refuses a fill the template refuses with every finding, and
otherwise renders the title and the body from the fields and writes them. Every caller that writes
through kern passes the same check: an agent host through the MCP door, a script through a
command, another component through the verbs.

| verb | asks | answers |
|---|---|---|
| `issue-create` | `github.issue-create`, `{"owner", "repo", "template", "fields", "labels"?}` | `github.written` |
| `issue-comment` | `github.issue-comment`, `{"owner", "repo", "number", "template", "fields"}` | `github.written` |

- **`github.written`** is `{"repo", "number", "url", "read", "via", "data"}`: the issue filed or
  commented on, the link to what was written, when, through which backend, and GitHub's answer.
- **No free text.** A request with any other key, a `body` or a `title` among them, is refused
  before the template is read.
- **The token** posts to REST (`POST /repos/<owner>/<repo>/issues`, `.../issues/<n>/comments`),
  waiting out the pace and bounded by `RequestWait` on the clock, as a read is. A rate limit
  refuses a write before GitHub takes it, so it is sent again, four times at most.
- **The relay** runs the write as a [step](#the-verb-step-a-write-tool-through-the-relay) of the
  session's own tool: `issue_write` with `method = "create"`, or `add_issue_comment`, with the
  rendered title and body.

### The commands, and the door's writing tool

`github issue-create --repo OWNER/REPO --template NAME --fields JSON [--label LABEL]...` and
`github issue-comment --repo OWNER/REPO --number N --template NAME --fields JSON` are writing
commands (`writes = true`), which kern's MCP door serves as the tool `github_write` (kern's README
"The MCP door"). Each asks its verb on a request port of its own, `file-issue` and `file-comment`,
which the core delivers like any request: the ports are bound to the component's own verbs where
the wiring binds them to nothing (`index.RequestPort.Self`). `--fields` is the fields as one JSON
object; the answer names the issue and the link.

### Templates

A template is data, `<name>.json` in the folder the config key `templates` names (`.se/templates`
by default, relative to the checkout). The application's own template engine writes it, so kern
checks a fill without calling that engine. Each write reads it again, so a regenerated template
counts at once.

- **What kern reads:** `form`, the template's name, which must be the file's; `field_shape`, each
  field's shape in order; `schema`, a JSON Schema 2020-12 of the fields. Other keys are read past.
  - SE's `forms.py draft <form> --json` writes all three.
  - Beside them, kern alone reads `markers` (the lines the body opens with), `actions` (the writes
    the template serves, `issue.create` or `comment.create`; none names both) and `level` (its
    headings' level, 2 by default).
- **A field's shape:** `kind`, one of `line`, `choice`, `text`, `list`, `issue`, `checklist`,
  `object` and `code`. Beside it: `heading`, `required`, `none_ok` (`None` fills it), `choices`,
  `max_words`, `numbered`, `label` (a `<label>: <value>` line above the sections), `source = "title"`
  (the issue's title, never in the body) and `lang` (a code block's).
- **The check:** the fields against the schema's `type`, `properties`, `required`,
  `additionalProperties`, `items` and `enum`. Then each field's shape: a required field filled,
  `None` only where `none_ok` lets it stand, a choice among its choices, at most `max_words` words.
  An issue's template needs a title field, and one whose `actions` lack the write is refused.
  - A template engine's own rules beyond its schema and shapes, such as its prose rules, do not
    reach kern: the application checks those where it renders.
- **The render:** the markers, the label lines, then each body field with a value under its
  heading, in the template's order: text as it stands, a list numbered or bulleted (an item that
  is one whole HTML comment kept as it is), a checklist as `- [x]` items, an object as `key: value`
  lines, code fenced.

## Telling the agent, on the hook

On each `PostToolUse`, `UserPromptSubmit` and `SessionStart` the component tells the agent the
tool calls `outbox` waits for, reads and steps, oldest first, each with its note, as the host's
hook `additionalContext`. A PostToolUse that answered a step first waits up to `tell-wait`
(`300ms`) for the next step, so the agent is told it in the same hook: a step asked after that
answer, never one asked before it.

## Verbs and names

| verb | asks | answers |
|---|---|---|
| `issue` | `{"owner", "repo", "number"}` | the issue's snapshot |
| `pull` | the same | the pull request's snapshot |
| `check-runs` | the same | the snapshot of the check runs at the pull request's head |
| `comment-list` | the same | the snapshot of every comment of the issue or pull request, every page |
| `issue-list` | `{"owner", "repo", "state", "labels"}` | the list of the repository's issues in that state carrying every label |
| `pull-list` | `{"owner", "repo", "state"}` | the list of the repository's pull requests in that state |
| `step` | `github.step` | `github.step-answer`, the tool's answer |
| `issue-create`, `issue-comment` | a filled template ([Writes](#writes-filled-templates-only)) | `github.written` |

`state` is `open` (the default), `closed` or `all`; `labels` lists issues alone, since GitHub
lists pull requests by none. A list read keeps the issues, not the pull requests GitHub's issues
list also holds.

A **snapshot** is `{"repo", "number", "read", "via", "data"}` and a **list** is
`{"repo", "state", "labels", "read", "via", "data"}`: `read` is the time it was read, `via` the
backend, `data` the answer as that backend read it (GitHub's REST JSON through the token, the
tool's JSON through the relay — a list's `data` is every page's items in GitHub's order). A
snapshot stands at the key `<owner>.<repo>.<number>` and a list at `<owner>.<repo>.<state>`, then
each label in order, all in lower case, in its family:

```toml
[instances.github]
component = "github"
# config = { backend = "auto", refresh = "2m", relay-wait = "0s", pace-max = "60s", step-wait = "60m", tell-wait = "300ms", templates = ".se/templates" }

[wires]
"github.second" = "clock/second"          # the refresh's count of seconds; "built-in" refreshes nothing
"github.issues" = "github/issues/<key>"
"github.pulls" = "github/pulls/<key>"
"github.checks" = "github/checks/<key>"
"github.comments" = "github/comments/<key>"
"github.issue-lists" = "github/issue-lists/<key>"
"github.pull-lists" = "github/pull-lists/<key>"
"github.outbox" = "github/outbox/<key>"
"<component>.<request port>" = "github.issue"
"<component>.<request port>" = "github.step"
```

An outbox entry stands at `<verb>.<key>`, a step's at `step.<tool>.<hash of its input>`. The component runs one instance per core. It is switched
off with `enabled = false`: it then looks for no token and hears nothing, and its names read their
built-in values.

## Its forbid list

The component forbids every other component reaching GitHub past it (`github.Forbidden`, kern's
README "Forbid lists"): the command `gh`, the host `api.github.com`, and the imports
`github.com/google/go-github` and `github.com/cli/go-gh`. Only its own folder may use them.

## Claims and its hot test

`expect.toml` claims `github.issues` and the read verbs' requests, `github.issue`, `github.pull`,
`github.check-runs`, `github.comment-list`, `github.issue-list` and `github.pull-list`; `step`,
which only the relay runs, and the write verbs `issue-create` and `issue-comment`, whose hot test
would write to GitHub, are claimed by none. `hot_test.go` meets every expectation the
checkout's readers pool under them, against the fake REST server in the normal suite and against
GitHub itself in its hot test (kern's README "Hot tests"):

```sh
KERN_HOT=1 go test -count=1 -run '^TestHot$' ./components/github/
```

It reads the checkout's home from its origin and the token the component finds
(`KERN_HOT_GITHUB_API` names another API). A reader's ask names the owner `owner` and the
repository `repo`, which the hot test asks as the home's; every answer is written back with the
home as `owner/repo` and `owner.repo`, so no recording names it. The given `issue 1 is read` reads
the home's issue 1.

## Tests

`github_test.go` and `lists_test.go` hold both backends to fakes: the token backend to a fake REST
server (per-page ETags, the refresh driven by a second's count the case writes, a check-runs read at the head, each kind of rate limit on a
single read and on a later page, paged issue, pull-request and comment reads), the relay to
PostToolUse payloads shaped as a cloud session's hook hands them (list_issues' cursor pages,
list_pull_requests' and get_comments' numbered pages, a list heard whole without being asked), and
both through the real core, from the
hook's post to a client's read of the snapshot name. `step_test.go` holds the verb `step` and the
telling: a step waits for the tool use that matches it and answers that tool's answer, and the hook
of an answer tells the next step as the host's hook JSON. `write_test.go` holds the writes: a comment
rendered from each kind of field, the relay's `issue_write` step, a request with free text refused,
and templates read or refused. `door_test.go` files an issue through kern's MCP door and the real
core against the fake REST server, and holds a refused fill, free text and an absent template to
reaching GitHub not at all.
