# files

kern's `files` component is the I/O component whose outside thing is the disk under the managed
folders an application names in its settings. It holds every managed file in memory as names in
the index, the warm model: reads and searches answer from it, and a write names the version it read.
This is the person's decision of 22 Nov (deck "The warm model", option B).

The tools that do the heavy reading and writing go through it. A hand edit, and a host without
hooks, still write the disk directly, which is acceptable: the watcher and the hash reconcile keep
the model in step, and compare-and-swap refuses a write over a change its writer did not see.
Outside the managed folders nothing changes.

> I should still be able to edit a file by hand. That will never go through the warm model. But
> because we have compare and swap, and also because we have file watchers on the files that we
> have in the model, that's okay.

## The model

| output | value |
|---|---|
| `model` | a family: each managed file at its path's key, `{"path", "hash", "size", "held", "content"}` |
| `watching` | whether the watcher reported the start's cookie in every managed folder |

- **A file** is its path relative to the checkout's root, slash-separated; its content's SHA-256 in
  hex, `hash`, which is the version a write names; its size; and, where `held`, its content. A text
  file no larger than `content-limit` is held; a larger one, or a binary one (it holds a NUL byte),
  is held by its hash alone.
- **Its key** is its path with each byte a family key may not hold written as `_` and two hex
  digits: `docs/a_b.md` stands at `docs_2fa_5fb.md` (`files.Key`, and back `files.PathOf`).
- **What it holds**: every regular file under a managed folder, past the folders `.git` and
  `node_modules` inside one, and past the component's own files, whose names start
  `.kern-files-` (the start's cookie and a write's temporary file). A symbolic link is not held.

| config | built-in | what |
|---|---|---|
| `folders` | `[]` | the managed folders, relative to the checkout's root; each must stand at the start, else the start is refused naming it |
| `content-limit` | `262144` | the largest file, in bytes, whose content the model holds |
| `cookie-wait` | `2s` | how long the watcher has to report the start's cookie |

```toml
[instances.files]
component = "files"
config = { folders = ["docs", "notes"] }
# config = { folders = [], content-limit = 262144, cookie-wait = "2s" }

[wires]
"files.model" = "files/model/<key>"
"files.watching" = "files/watching"
```

## Verbs

| verb | asks | answers |
|---|---|---|
| `read` | `{"path"}` | the file with its content |
| `list` | `{"folder"}`, `""` for every managed folder | `{"files"}`, each without content, by path |
| `search` | `{"pattern", "folder"}`: a Go regular expression, matched line by line | `{"matches": [{"path", "line", "text"}], "more", "unsearched"}` |
| `write` | `{"path", "content", "version"}` | the file written, without content |
| `remove` | `{"path", "version"}` | `{"path"}` |

- **Reads answer from the model.** `read` answers a held file from memory; one held by its hash
  alone is read from the disk. `list` and `search` read the model alone; `search` names in
  `unsearched` the files under the folder it did not read, held by hash alone, and stops at 1000
  matches with `more` set.
- **A write names the version it read**, `""` for a file that does not stand yet. The module
  hashes the file on the disk at the write, so a hand edit the watcher has not reported yet refuses
  it too. A stale one is refused: `files write docs/a.md: the file changed since version <v> was
  read, and is <h> now: re-read it and write again`, and the refusal brings the model in step with
  the disk, so the re-read answers the hand edit. A write that holds writes a temporary file beside
  the file and renames it into place, keeping the file's mode, and makes its folders.
- **A path outside every managed folder** is refused, naming the folders: the module does not
  reach it, and its caller reads and writes it on the disk.

## Commands and the door

The module serves its verbs to an agent through four commands, whose request ports are bound to its
own verbs (`index.RequestPort.Self`), so the [MCP door](../../README.md#the-mcp-door) serves them as
the tools `files` (reads) and `files_write` (writes), named after the instance:

| command | tool | what |
|---|---|---|
| `files read --path PATH` | `files` | the file from the model: its first line `PATH at version <hash> (<n> bytes):`, the rest its content |
| `files edit --path PATH --version V --old TEXT --new TEXT [--all]` | `files_write` | replaces `--old`, which must occur once (or each, with `--all`), in the file standing at version V; a missing or repeated text is refused |
| `files write --path PATH --version V --content TEXT` | `files_write` | writes the whole file; `--version ""` for a new one |
| `files remove --path PATH --version V` | `files_write` | removes the file |

Each write names the version `files read` answered and is refused, naming re-read, where the file
changed since, as the verbs are.

## The guard

The module guards the host's `PreToolUse` (`index.Guards`): a tool call that writes a managed file,
or a managed folder or one that holds one, is denied, and its reason names the files it writes,
the managed folders and the tools above, `files read` first; every other call passes. What a call
writes is `host.ToolCall.Writes`, from Claude Code's and the Copilot CLI's tools and a parsed shell
command ([kern's README "Agent tools"](../../README.md#agent-tools)). Writes into kern's own folder
`.se/` always pass, and without a started instance nothing is refused.

## In step with the disk

Reused from quackitect v4 (`src/engine/indexwatch.go`) and v5 (`src/modules/files/watch.go`):

- **The watcher** is the operating system's (fsnotify), behind the interface `files.Watcher`. It is
  not recursive: the start adds every managed folder and each folder under it, and a folder made
  later is added when its creation arrives. Each path it hands is read from the disk again: a
  changed file is written, a new folder is watched and walked, and a path gone is dropped with
  everything under it.
- **The hash reconcile** hashes every file of every managed folder, writes what changed and drops
  what is gone. It runs at the start, after the watcher opens, so a change during it is heard
  after it, and whenever the watcher reports an overflow (`files.ErrOverflow`), since it lost
  events. Its commits carry 8 MiB of content at most each, under the bus's cap.
- **A deaf watcher is found out at the start.** Some mounts deliver no events. The start writes a
  cookie, `.kern-files-cookie`, in each managed folder, and `watching` turns true once the watcher
  reported every one, each removed once heard. If `cookie-wait` passes first, measured on the
  host's clock (`host.Clock`), the model is unwatched: `watching` stays false, the cookies are
  removed, and `read` reads the disk, and `list` and `search` reconcile first, the cold path.

## The forbid list

Every I/O module declares what only it may use (kern's README "Forbid lists"). The files module's
list is `files.Forbids`: the direct file writes of Go's package `os` and their Python
counterparts, declared with `index.Forbids` on its config `folders` (`on = "folders"`). They are
forbidden to every other component on the managed folders: `kern forbids` refuses a component's
use of one unless it declares the folders it writes (`index.Writes`) and none of them is a folder
the checkout's wiring manages, naming the call and the managed folders.
`TestTheForbidListNamesTheModulesOwnWrites` holds the list to every write of package `os` the
module makes itself.

## Tests

`files_test.go` drives the module with a watcher the case feeds by hand and a clock it fires, over
a temporary folder, so no case waits for the disk to be noticed. Each case proves one of these:

- the model holds the managed files, past `.git`, a binary file and one above the limit by hash
  alone;
- a read answers from memory while an unheard change stands on the disk;
- a hand edit, a new file, a new folder and a removed file show once heard;
- a write that names an old version is refused, naming re-read, also before the watcher reported
  the hand edit;
- an overflow reconciles what the watcher missed;
- a deaf watcher reads the disk;
- a write to a managed file is refused, in both hosts' formats, by Write, Edit and a parsed shell
  command, naming the tools, and a write elsewhere, a read, `.se/` and another event pass
  (`route_test.go`);
- the door's commands read, edit, write and remove through the model, refuse a stale version, and
  the door serves them as `files` and `files_write`.

One case starts the module on a wiring with the operating system's watcher, and a hand edit on the
real disk reaches its name. Its outside thing is the disk, so the normal suite's run against a
temporary folder is its run against the real service, and it has no separate hot test.
