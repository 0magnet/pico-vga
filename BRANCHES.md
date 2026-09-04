# Branches

Six working copies of this project existed on two machines, none of them
pushed anywhere. They were gathered into this repository on 2026-08-21, one
branch per copy, so they can be flashed and compared on real hardware instead
of guessed at.

## What the histories showed

Measured against the newest copy, four of the six collapsed to almost nothing:

| original copy | commits | unique commits | outcome |
|---|---|---|---|
| newest trunk | 55 | — | became `main` |
| workstation copy | 49 | **1** | `node` |
| a `.bak` copy | 26 | **14** | `mainframe-bak` |
| a `.bak1` copy | 28 | **1** | `mainframe-bak1` |
| two further copies | 24, 26 | **0** | dropped — every commit already in `main` |

So the real content is the trunk plus 16 commits, 14 of them on one branch.
The two dropped copies contained nothing that is not reachable from `main`.

## The branches

| branch | what it is |
|---|---|
| `main` | newest trunk, plus this file and the README |
| `mainframe-bak` | **the interesting one** — 14 commits the trunk does not have |
| `mainframe-bak1` | one unique commit: black-line fix + color cycling in `color_run` |
| `node` | one unique commit: 1BPP renderer cleanup, larger 640x480 scanline buffers |
| `*-wip` | uncommitted edits found in that copy, as one commit on top |

## Where to start

`mainframe-bak` split off early and went its own way. Its commits read like
working milestones:

    b20a0ba  Working composable scanlines: 640x480 VGA output!
    b40b0f4  Two-SM PIO RGB: smooth edges achieved
    2d232f6  Working VGA color bars with PIO HSYNC timing
    bd489e4  PIO-based RGB output with 8 color bars (7 pulled + black)
    a791d68  Fix: Use RGB555 format for Pimoroni VGA board
    41300e5  Fix: Use goroutine for render loop (runs on core1)

Meanwhile the trunk's most recent commit is **"Revert to stable single-core
mandelbrot"**. The trunk may therefore have backed out VGA output that this
branch still drives correctly. Flash `mainframe-bak` before assuming `main` is
ahead.

Suggested order:

1. `mainframe-bak` and `mainframe-bak-wip` — most likely to show a picture
2. `main` / `mainframe-wip` — the trunk baseline
3. Cherry-pick the single commits from `node` and `mainframe-bak1` if still wanted
4. Decide what `main` should be, once something demonstrably works

## About the `-wip` branches

These hold edits that were sitting uncommitted in a working copy — a snapshot
to mine, not a good state. They include binary artifacts (`hello.uf2`
firmware), captured with `git diff --binary`; a plain `git diff` emits an
unappliable stub for binaries and then aborts the whole patch, taking the text
changes with it. If you regenerate these, keep `--binary`.
