# ADR 0054: Restart PRRTE Jobs Inside Their Slurm Allocation

Status: proposed

Date: 2026-10-29

## Context

A `RESTARTED` Advice is applied in Slurm as a requeue: the job gives back all
of its nodes, Slurm keeps the requeued job ineligible until its launch
credential expires (`cred_expire`, 120 s by default), and the job then waits in
the queue until all of its nodes are free at once. One failed node therefore
costs the whole job a full queue wait, which grows with the job size and the
machine load.

A job whose processes run under a persistent PRRTE DVM can instead replace the
blamed node inside its own allocation: it stops the node's PRRTE daemon, asks
Slurm for one more node through `PMIx_Allocation_request` (PRRTE allocates it as
a separate Slurm job, the grow job), and relaunches its application from its
own checkpoint on the whole DVM. The job keeps its other nodes and its place.

Slurm only lets root or the job's owner create steps in a job. The controller
runs as `SlurmUser`, so it cannot carry out such a restart itself, and it has no
authenticated channel to a job's DVM.

## Decision

### Apply restart Advice inside the allocation for opted-in PRRTE jobs

When `SLURM_ADVISOR_RESTART_IN_ALLOCATION_ENABLED` is true, SAC applies a
`RESTARTED` Advice for a running attempt of a job that declares
`"in_place_recovery": "prrte"` in its APS spec by ordering the job to restart
inside its allocation. Every other job, and every job while the option is off,
keeps the requeue path of [ADR 0045](0045-require-advice-before-slurm-requeue.md).
The option is off by default.

### SAC orders, the job carries out the order

SAC writes the order into the job's `AdminComment`, keeping the site's other
content, as the token
`nvr-restart-in-allocation:<generation>:<attempt>:<issued unix seconds>:<nodes>`,
where `<nodes>` are the blamed nodes the job still holds, and sends `SIGUSR2` to
the batch script with `scancel --batch`. Only operators and `SlurmUser` can
write `AdminComment`, so a job cannot forge an order. The job carries the order
out as its owner and marks each grow job with
`Comment=prrte-grow-of=<job ID>.<restart count>`.

### Converge only from observed scheduler state

SAC concludes the restart only when it observes a marked grow job for the
ordered attempt, and then reports `CONVERGED` with the new reason
`RESTARTED_IN_PLACE`, because the successor attempt that proves a requeue never
appears. Command acceptance is never convergence, consistent with
[ADR 0025](0025-filter-and-terminally-reconcile-actionable-advice.md). The step
is derived from fresh state on every pass, so a controller restart resumes it.

### Fall back to requeue

SAC requeues the attempt instead when the order is not carried out within
`SLURM_ADVISOR_RESTART_IN_ALLOCATION_TIMEOUT` (2 minutes by default), and when
the attempt was already restarted in place (see
[ADR 0055](0055-count-a-restart-in-place-as-a-restart-of-its-attempt.md)).

### Release grow jobs the job no longer uses

A grow job outlives its job when the job ends without shutting down its
runtime, for example on requeue. SAC cancels marked grow jobs once their
attempt is superseded or stops running, in act mode only.

### Extend the contract

The Advisor API gains the `SLURM_RESTART_IN_ALLOCATION` scheduler action, the
`RESTARTED_IN_PLACE` reconciliation reason, and the `InPlaceRecovery` job
recovery configuration with the single value `PRRTE`.

## Consequences

- A failed node costs the job one node acquisition instead of a requeue: no
  credential-expiry delay and no queue wait for the whole job. The job holds its
  other nodes idle until the replacement arrives, and keeps the blamed node,
  quarantined, until it ends.
- The job side is a contract, not code in the controller: the job must trap the
  signal, carry out the order, and mark its grow jobs. The reference
  implementation is described in
  [ADR 0056](0056-provide-the-job-side-of-restart-in-allocation-as-a-wrapper.md).
- The restart depends on PRRTE fixes that are not in an upstream release yet.
- SAC writes `AdminComment`, signals batch scripts, and cancels grow jobs:
  scheduler actions that need human review and are audited as
  `slurm.restart_in_allocation`.
- The operator contract is documented in
  [docs/operators/slurm.md](../operators/slurm.md).

## Alternatives Rejected

### Run the restart as a step from SAC

`srun --jobid` from SAC into another user's job fails (`Invalid user id`).
Running SAC as root would hide that constraint and widen its privileges.

### Talk to the job's DVM over PMIx from SAC

There is no authenticated channel from an outside service to a job's DVM, and
it would make SAC depend on the job's runtime.

### Converge when the order is accepted

Accepting `scontrol` and `scancel` does not show that the job restarted. It
would also report success for a job that lost the signal or got no node.
