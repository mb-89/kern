# The clock

kern's `clock` component is the I/O component whose outside thing is time. It writes time as two
names, and a component that acts on time reads the one it needs and runs when it changes, as for
any input. No component keeps a timer of its own. This is the person's decision of 22 Nov: time is
an I/O module.

| output | moves | value |
|---|---|---|
| `tick` | 10 times a second | the time of the last tick, UTC, on the 100 ms |
| `second` | once a second | the time of the last second, UTC, on the second |

These two are the granularity: a component never runs more often than the tick. One that can be
slower reads the second.

```toml
[instances.clock]
component = "clock"

[wires]
"clock.tick" = "clock/tick"
"clock.second" = "clock/second"
"<component>.second" = "clock/second"
```

## A component that acts on time

- **It reads the second, or the tick.** It declares `index.Reacts` with an input struct that reads
  the name (`Second time.Time` tagged `index:"second"`), and the wiring binds that input to
  `clock/second`. The index runs the reaction after each commit that moves the name, outside its
  lock, in the clock's goroutine: it returns quickly and starts long work on its own.
- **It keeps its own interval as configuration** and a `clock.Relay` at the reaction's start:
  `Due(now, every)` answers at once whether `every` has passed since its last real run, so the
  reaction returns while it is short and runs whole when it is not. The first time it is asked
  starts the interval; an interval of 0 or less is never due.
- **A wiring may leave the input built-in.** It then reads the zero time and never moves, so the
  reaction never runs. The [GitHub component](../github/README.md)'s refresh is the worked
  example.

## Its loop

The start runs one loop per instance. It waits on the host's clock (`host.Clock`, `host/clock.go`)
until the next 100 ms, commits the tick, and the second where it moved. A wait that overran skips
the ticks it missed: the next tick is the one after the time it woke at. The loop ends when its
commit fails, as once its core is gone.

The clock component and the host's clock are the only places in kern that may start a timer:
`kerntest/timers_test.go` refuses a `time.Tick`, `time.NewTicker` or `time.AfterFunc` anywhere
else. A wait for an answer measures its limit against the host's clock interface, so a test drives
it with a fake clock and never waits.

## Tests

`clock_test.go` drives the loop with a fake host clock that the case moves and fires: the tick on
the 100 ms, the second on the second, and an overrun that skips the ticks it missed. The relay's
own case holds it to its interval. A test passes its own clock through `clock.Options` to
`clock.New`.
