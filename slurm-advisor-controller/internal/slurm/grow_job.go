// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurm

import (
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/cluster"
)

// growJobMarker prefixes the comment of a grow job. PRRTE allocates the nodes
// a job grows by as a Slurm job of their own (Slurm cannot extend a running
// job), and the restart-in-allocation command writes
// "prrte-grow-of=<job ID>.<restart count>" into it once granted. Nothing else
// links the two jobs, and a grow job outlives a job that ends without shutting
// its runtime down, such as one requeued by a failed restart.
const growJobMarker = "prrte-grow-of="

// growJobParent returns the job attempt a grow job was allocated for, or nil
// when the comment carries no well-formed grow marker. A job attempt is its
// Slurm job ID and restart count, the attempt index of an active job.
func growJobParent(comment string) *cluster.JobAttemptRef {
	value, ok := strings.CutPrefix(comment, growJobMarker)
	if !ok {
		return nil
	}
	jobID, attempt, ok := strings.Cut(value, ".")
	if !ok || jobID == "" {
		return nil
	}
	if _, err := strconv.ParseUint(jobID, 10, 64); err != nil {
		return nil
	}
	attemptIndex, err := strconv.ParseInt(attempt, 10, 32)
	if err != nil || attemptIndex < 0 {
		return nil
	}
	return &cluster.JobAttemptRef{JobID: jobID, AttemptIndex: int32(attemptIndex)}
}
