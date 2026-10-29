// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"

	"github.com/cockroachdb/errors"
	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/cluster"
	"go.uber.org/zap"
)

// reconcileGrowJobs cancels the grow jobs whose job attempt no longer runs.
//
// A restart inside the allocation can add nodes that the scheduler grants as a
// separate grow job. The job's runtime gives them back when it shuts down, but
// not when the job ends without that shutdown: a requeue after a failed
// restart, a cancellation, or a lost batch host. The grow job then keeps its
// nodes until the parent's time limit. The decision is taken from fresh state
// alone: a grow job is cancelled when the latest observed attempt of its
// parent is a different attempt, or the same attempt has stopped running.
// Grow jobs of a job this controller does not observe are left alone, as is
// every grow job of a job whose mode is not act.
func (c *Controller) reconcileGrowJobs(ctx context.Context, snapshot ControllerSnapshot, observation *SyncObservation) error {
	if len(snapshot.GrowJobs) == 0 {
		return nil
	}
	jobsByID := latestJobsByIDFromSnapshot(snapshot)

	var errs []error
	for _, grow := range snapshot.GrowJobs {
		if err := ctx.Err(); err != nil {
			errs = append(errs, errors.Wrap(err, "reconcile grow jobs"))
			break
		}
		parent, ok := jobsByID[grow.GrowOf.JobID]
		if !ok || growJobStillUsed(parent, *grow.GrowOf) {
			continue
		}
		mode := modeDecisionForJob(snapshot, c.modePolicy(), parent).Mode
		if mode == cluster.JobModeIgnore {
			continue
		}
		if mode != cluster.JobModeAct {
			observeGrowJobCancel(observation, mode, ObservationOutcomePlanned)
			continue
		}
		if err := c.ActionExecutor.JobKill(ctx, grow.JobID); err != nil {
			observeGrowJobCancel(observation, mode, ObservationOutcomeError)
			errs = append(errs, errors.Wrapf(err, "cancel grow job %s of job %s", grow.JobID, parent.JobID))
			continue
		}
		observeGrowJobCancel(observation, mode, ObservationOutcomeSuccess)
		if c.Logger != nil {
			c.Logger.Info(
				"cancelled grow job of a job attempt that stopped running",
				zap.String("grow_job_id", grow.JobID),
				zap.String("job_id", parent.JobID),
				zap.Int32("grown_attempt_index", grow.GrowOf.AttemptIndex),
				zap.Int32("attempt_index", parent.AttemptIndex),
				zap.String("state", string(parent.State)),
			)
		}
	}
	return errors.Join(errs...)
}

// growJobStillUsed reports whether the grown attempt may still hold its
// allocation: it is not superseded by a later attempt and has not stopped
// running. What fresh state cannot settle keeps the grow job, an attempt not
// observed yet or a state not known, because cancelling the nodes of a running
// job is worse than holding idle ones for one more pass.
func growJobStillUsed(parent cluster.JobExecutionRecord, grown cluster.JobAttemptRef) bool {
	if parent.AttemptIndex != grown.AttemptIndex {
		return parent.AttemptIndex < grown.AttemptIndex
	}
	return parent.State == cluster.JobStateUnspecified ||
		(!isFinalJobState(parent.State) && parent.State != cluster.JobStateRequeued)
}
