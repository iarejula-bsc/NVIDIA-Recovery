# ADR 0056: Provide the Job Side of Restart in Allocation as a Wrapper

Status: proposed

Date: 2026-10-29

## Context

The job side of [ADR 0054](0054-restart-prrte-jobs-inside-their-slurm-allocation.md)
must run inside the job, as its owner, on the node that runs the batch script
and the PRRTE DVM: start the DVM, trap `SIGUSR2`, read the order, quarantine
the blamed nodes, grow the DVM back, mark the grow job, and relaunch the
application. Asking every user to write that logic in their batch script
repeats it in every job, and getting any of it wrong breaks the contract
silently.

The PMIx and PRRTE command-line tools already expose the needed operations:
`pquery` reads the nodes the DVM spans, and `palloc --extend` makes PRRTE
allocate replacement nodes.

## Decision

### Ship a reference wrapper with NVIDIA Recovery

NVIDIA Recovery provides `deploy/slurm/nvr-prrte-run`, a shell wrapper that
replaces `prun` in the batch script:

```bash
exec nvr-prrte-run -n 4 --map-by ppr:1:node python train.py --resume
```

It implements the whole job side of the contract with `prte`, `prun`, `pterm`,
`pquery`, and `palloc`. It is the reference implementation: a site may use
another runtime-specific implementation that honors the same order, signal,
and grow-job marking.

### Relaunch only on an order

When the application fails, the wrapper waits for a restart order and
relaunches the application only after carrying it out. NVIDIA Recovery is the
only component that decides a recovery: a relaunch of its own would run again
on the faulty node until the order arrives, and would keep the allocation busy
when no order comes. The wait is bounded by `NVR_PRRTE_ORDER_TIMEOUT`, which
must cover detection, attribution, and Advice. When it expires, or when the
application fails again after the restart, the wrapper exits with an error and
the failed job follows the usual recovery.

### Require exec

The batch script must end with `exec nvr-prrte-run`. `scancel --batch` signals
only the batch script's process, and signalling every step would also reach the
PRRTE daemons and the application.

### Test it end to end with PRRTE master

Restart in allocation needs PRRTE fixes that are in PRRTE master but not in a
release yet. The opt-in E2E run (`E2E_RESTART_IN_ALLOCATION`) builds a slurmd
image with PMIx and PRRTE master at pinned commits, adds dedicated Kind workers
and Slurm nodes, and runs the job through `nvr-prrte-run`. The scenario stays
out of default CI because the image takes several minutes to build.

## Consequences

- Users change one line of their batch script; the application only needs to
  resume from its own checkpoint.
- NVIDIA Recovery maintains a small job-side script in addition to the
  controller, and its behavior is covered by the E2E scenario rather than unit
  tests.
- `palloc` does not report the grow job's ID, so the wrapper finds the grow job
  as the user's unmarked PRRTE job on the node that joined the DVM.

## Alternatives Rejected

### A compiled helper linked against PMIx

It needs building per site and toolchain, and duplicates what the PMIx tools
already provide.

### Add the tool to PRRTE

The order format and the grow-job marking belong to the NVIDIA Recovery
contract, not to the runtime.

### Poll `AdminComment` instead of using a signal

It removes the `exec` requirement but adds periodic `slurmctld` load per job
and reacts later.
