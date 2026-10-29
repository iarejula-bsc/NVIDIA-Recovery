# ADR 0055: Count a Restart in Place as a Restart of Its Attempt

Status: proposed

Date: 2026-10-29

## Context

NVIDIA Recovery models recovery per job attempt: the anomaly identity, the
attribution claim, the recovery sequence, and the requeue limit are keyed by
the attempt, and a requeue proves itself through a successor attempt.

A restart inside the allocation
([ADR 0054](0054-restart-prrte-jobs-inside-their-slurm-allocation.md)) keeps the
attempt running. Without a rule for that case, the restart limit does not see
it, a later failure of the same attempt finds its attribution already claimed,
and a restart Advice that answers that later failure has the same desired state
as the converged one, so no controller would act on it. Attributing the same
attempt again while it keeps running would also answer the failure already
repaired and could terminate a healthy job.

## Decision

### Restart an attempt in place at most once

SAC restarts an attempt inside its allocation at most once. A later restart
Advice for an attempt that already holds a grow job is applied as a requeue
into a fresh attempt. The job side ends the job with an error when its
application fails again after a restart in place.

### Record the restart on the attempt

When a reconciliation status converges with `RESTARTED_IN_PLACE` for the
current Advice generation, Job Manager marks the attempt `restarted_in_place`
and reopens its attribution claim, once per attempt through a conditional
update. Only that path sets the field.

### Count it towards the restart limit

The requeue limit and the checkpoint-based stop count marked attempts in
addition to the attempt index, so a restart in place costs the same as a
requeue.

### Attribute the attempt again only when it ends

Job Manager defers attribution of an attempt restarted in place until the
attempt is final. When it ends, it is attributed like any failed attempt, and
the resulting `RESTARTED` Advice opens a new generation: the attempt ending
after a restart in place can only follow a new failure.

## Consequences

- A second failure of the same attempt is handled by the existing failed-job
  path: attribution, then `RESTARTED` or `TERMINATED` under the same limit.
- The job record gains the `restarted_in_place` column (Alembic revision
  `000000000006`).
- A job cannot restart in place twice without a requeue in between, even if
  both failures would be cheaper to repair in place.

## Alternatives Rejected

### Unlimited restarts in place per attempt

Every restart would need its own identity inside the attempt, which
anomaly, attribution, sequence, and limit keys do not have.

### Do not count restarts in place

A job failing repeatedly would never reach its restart limit.

### Attribute the running attempt again

It re-attributes the failure that was already repaired and can lead to
terminating a job that recovered.
