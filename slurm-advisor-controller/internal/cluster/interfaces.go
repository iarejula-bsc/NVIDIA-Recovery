// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cluster

import "context"

//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -destination=clustermock/mock_cluster.go -package=clustermock . JobReader,ActiveJobEnricher,JobMutator,Cluster

type JobReader interface {
	ActiveJobs(ctx context.Context) ([]JobExecutionRecord, error)
	// FinalizedJobs returns fresh records for the requested attempts. Successful
	// lookups remain usable when other lookups fail; the returned error joins
	// those failures. Missing records with an error are not evidence of absence.
	FinalizedJobs(ctx context.Context, records []JobAttemptRef) ([]JobExecutionRecord, error)
}

// ActiveJobEnricher adds best-effort scheduler metadata to active jobs after
// reconciliation has selected the attempts it manages.
type ActiveJobEnricher interface {
	EnrichActiveJobs(ctx context.Context, jobs []JobExecutionRecord) []JobExecutionRecord
}

type JobMutator interface {
	JobKill(ctx context.Context, jobID string) error
	JobHold(ctx context.Context, jobID string) error
	JobRelease(ctx context.Context, jobID string) error
	JobSignal(ctx context.Context, jobID, signal string) error
	JobNodeUpdate(ctx context.Context, jobID string, exclusions, inclusions []string, nodeCount int) error
	JobRequeueHold(ctx context.Context, jobID string) error
	JobUpdateEligibleNow(ctx context.Context, jobID string) error
	// JobOrderRestartInAllocation orders a running job to restart inside the
	// allocation it holds, instead of being requeued. The job carries out the
	// order itself, as its owner: it isolates the quarantined nodes so the
	// scheduler cannot grant them again, replaces them and any node it already
	// lost with new ones, and relaunches its application. The order is recorded
	// in scheduler state, where later observations read it back as the job's
	// RestartOrder, and the job is woken to act on it. Ordering again replaces
	// the previous order.
	JobOrderRestartInAllocation(ctx context.Context, jobID string, order InPlaceRestartOrder) error
}

type Cluster interface {
	JobReader
	ActiveJobEnricher
	JobMutator
}
