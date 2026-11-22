# The clock

kern's `clock` component is the I/O component whose outside thing is time. It writes time as names,
and a component that acts on time reads the one it needs and runs when it changes, as for any
input. No component keeps a timer of its own. This is the person's decision of 22 Nov: time is an
I/O module.

| output | moves | value |
|---|---|---|
| `time` | `per-second` times a second (10) | the time since the clock's start, a `time.Duration` on the granularity's step |
| `second` | once a second | the seconds since the clock's start, an `int` that counts up |
| `minute` | once a minute | the minutes since the clock's start, an `int` that counts up |
| `hour` | once an hour | the hours since the clock's start, an `int` that counts up |

`time` is the fine-grained output, and nothing reads it yet. A component that acts every 5 seconds
reads `second` and runs on every fifth count; one that acts hourly reads `hour`. A component never
runs more often than the granularity.

| config | built-in | what |
|---|---|---|
| `per-second` | `10` | the granularity: how often a second `time` moves, from 1 to 1000; a value outside refuses the start |

```toml
[instances.clock]
component = "clock"
# config = { per-second = 10 }

[wires]
"clock.time" = "clock/time"
"clock.second" = "clock/second"
"clock.minute" = "clock/minute"
"clock.hour" = "clock/hour"
"<component>.second" = "clock/second"
```

## Its forbid list

The clock forbids every other component reading or waiting on the system time (`clock.Forbidden`,
kern's README "Forbid lists"): in Go `time.Now`, `time.Since`, `time.Until`, `time.Sleep`,
`time.After`, `time.Tick`, `time.NewTicker`, `time.NewTimer` and `time.AfterFunc`; in Python
`time.time`, `time.time_ns`, `time.sleep`, `time.monotonic`, `time.monotonic_ns`,
`time.perf_counter`, `datetime.now`, `datetime.utcnow`, `datetime.today` and `date.today`. Only
its own folder may use them; the host's clock (`host.Wall`) is the one other reader, and a
component that stamps a time, as the session record does, reads it there.

## A component that acts on time

- **It reads a counter.** It declares `index.Reacts` with an input struct that reads it (`Second
  int` tagged `index:"second"`), and the wiring binds that input to `clock/second`. The index runs
  the reaction after each commit that moves the name, outside its lock, in the clock's goroutine:
  it returns quickly and starts long work on its own.
- **It keeps its own interval as configuration** and a `clock.Relay` at the reaction's start:
  `Due(count, every)` answers at once whether `every` counts have passed since its last real run,
  so the reaction returns while they have not and runs whole when they have. The first count it is
  asked starts the interval; a count that jumps past it is due once, and the next interval counts
  from it; an interval of 0 or less is never due.
- **A wiring may leave the input built-in.** It then reads 0 for good, so the relay is never due.
  The [GitHub component](../github/README.md)'s refresh is the worked example.

## Its loop

The start runs one loop per instance. It reads the start's time from the host's clock
(`host.Clock`, `host/clock.go`), waits on it until the next step, and commits `time` and each
counter that moved. A wait that overran skips the steps it missed: the next step is the one after
the time it woke at. The loop ends when its commit fails, as once its core is gone.

The clock component and the host's clock are the only places in kern that may start a timer:
`kerntest/timers_test.go` refuses a `time.Tick`, `time.NewTicker` or `time.AfterFunc` anywhere
else. A wait for an answer measures its limit against the host's clock interface (`host.Within`
for a context), so a test drives it with a fake clock and never waits. The forbid list that
refuses every other reader of the system time is kern#18's.

## Tests

`clock_test.go` drives the loop with a fake host clock that the case moves and fires: `time` on
the step, each counter only when it moves, an overrun that skips the steps it missed, and the
configurable granularity. The relay's own case holds it to every fifth count. A test passes its
own clock through `clock.Options` to `clock.New`.
