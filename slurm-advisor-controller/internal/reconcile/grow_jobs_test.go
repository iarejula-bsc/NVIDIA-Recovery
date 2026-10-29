// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	pb "github.com/dsx-ai-factory/nvidia-recovery/api-gateway/gen/go/heimdall/v1"
	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/cluster"
	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/state"
	"go.uber.org/mock/gomock"
)

// growJob is the grow job 101 that attempt 0 of job 100 was given.
func growJob() cluster.JobExecutionRecord {
	return cluster.JobExecutionRecord{
		JobID: "101", JobName: "prrte", State: cluster.JobStateRunning,
		GrowOf: &cluster.JobAttemptRef{JobID: "100", AttemptIndex: 0},
	}
}

// A requeue after a failed restart kills the job's runtime before it can give
// its grow job back; the next pass sees the parent's new attempt and cancels
// the grow job, which is not tracked as a job of its own.
func TestSyncCancelsGrowJobOfRequeuedAttempt(t *testing.T) {
	requeued := managedJob(cluster.JobExecutionRecord{
		JobID: "100", WorkflowID: "workflow-100", AttemptIndex: 1, State: cluster.JobStatePending, Held: true,
		RecoveryConfig: &cluster.RecoveryConfig{InPlaceRecovery: cluster.InPlaceRecoveryPRRTE},
	})
	reader := newMockJobReader(t)
	expectActiveJobs(reader, []cluster.JobExecutionRecord{requeued, growJob()})
	executor := newMockJobMutator(t)
	executor.EXPECT().JobKill(gomock.Any(), "101").Return(nil)
	observer := &recordingObserver{}
	controller := Controller{
		JobReader:      reader,
		HeimdallClient: &fakeControllerClient{getJobsResponses: map[string]*pb.GetJobsResponse{"": {}}},
		ActionExecutor: executor,
		SeenJobs:       state.NewMemorySeenJobStore(),
		Observer:       observer,
		Options:        ControllerOptions{AdviceLoopTimeout: time.Hour, RestartInAllocation: true},
		ModePolicy:     ModePolicy{DefaultMode: cluster.JobModeAct, AllowAct: true},
	}

	result, err := controller.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, tracked := result.Snapshot.ActiveJobs[cluster.JobAttemptRef{JobID: "101"}]; tracked {
		t.Fatal("grow job tracked as a job of its own")
	}
	assertStageObservation(t, observer.observations[0].Stages, StageGrowJobReconciliation, ObservationOutcomeSuccess)
}

func TestReconcileGrowJobsCancelsOnlyGrowJobsOfStoppedAttempts(t *testing.T) {
	running := managedJob(cluster.JobExecutionRecord{JobID: "100", State: cluster.JobStateRunning})
	tests := []struct {
		name         string
		parent       *cluster.JobExecutionRecord
		grownAttempt int32
		wantCancel   bool
	}{
		{name: "attempt still running", parent: &running},
		{name: "parent not observed", parent: nil},
		{name: "attempt requeued", parent: withJob(running, func(j *cluster.JobExecutionRecord) { j.State = cluster.JobStateRequeued }), wantCancel: true},
		{name: "attempt superseded", parent: withJob(running, func(j *cluster.JobExecutionRecord) { j.AttemptIndex, j.State = 1, cluster.JobStatePending }), wantCancel: true},
		{name: "attempt finished", parent: withJob(running, func(j *cluster.JobExecutionRecord) { j.State = cluster.JobStateCancelled }), wantCancel: true},
		{name: "grown attempt not observed yet", parent: &running, grownAttempt: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := newControllerSnapshot(time.Now())
			if tt.parent != nil {
				addActiveJob(&snapshot, *tt.parent)
			}
			grow := growJob()
			grow.GrowOf.AttemptIndex = tt.grownAttempt
			snapshot.GrowJobs = []cluster.JobExecutionRecord{grow}
			executor := newMockJobMutator(t)
			if tt.wantCancel {
				executor.EXPECT().JobKill(gomock.Any(), "101").Return(nil)
			}
			observation := &SyncObservation{}
			controller := Controller{ActionExecutor: executor, ModePolicy: ModePolicy{DefaultMode: cluster.JobModeAct, AllowAct: true}}

			if err := controller.reconcileGrowJobs(context.Background(), snapshot, observation); err != nil {
				t.Fatal(err)
			}
			if got := len(observation.GrowJobCancels); got != map[bool]int{false: 0, true: 1}[tt.wantCancel] {
				t.Fatalf("cancel observations = %#v, want cancel %t", observation.GrowJobCancels, tt.wantCancel)
			}
		})
	}
}

// Observe mode never mutates the scheduler, including for grow jobs.
func TestReconcileGrowJobsOnlyPlansInObserveMode(t *testing.T) {
	snapshot := newControllerSnapshot(time.Now())
	addActiveJob(&snapshot, managedJob(cluster.JobExecutionRecord{JobID: "100", State: cluster.JobStateCancelled}))
	snapshot.GrowJobs = []cluster.JobExecutionRecord{growJob()}
	observation := &SyncObservation{}
	controller := Controller{ActionExecutor: newMockJobMutator(t), ModePolicy: ModePolicy{DefaultMode: cluster.JobModeObserve}}

	if err := controller.reconcileGrowJobs(context.Background(), snapshot, observation); err != nil {
		t.Fatal(err)
	}
	if len(observation.GrowJobCancels) != 1 || observation.GrowJobCancels[0].Outcome != ObservationOutcomePlanned {
		t.Fatalf("cancel observations = %#v, want one planned", observation.GrowJobCancels)
	}
}

// A failed cancellation is reported and retried on the next pass from fresh
// state; it does not stop the other grow jobs.
func TestReconcileGrowJobsReportsFailedCancellation(t *testing.T) {
	snapshot := newControllerSnapshot(time.Now())
	addActiveJob(&snapshot, managedJob(cluster.JobExecutionRecord{JobID: "100", State: cluster.JobStateCancelled}))
	second := growJob()
	second.JobID = "102"
	snapshot.GrowJobs = []cluster.JobExecutionRecord{growJob(), second}
	cancelErr := errors.New("scancel failed")
	executor := newMockJobMutator(t)
	executor.EXPECT().JobKill(gomock.Any(), "101").Return(cancelErr)
	executor.EXPECT().JobKill(gomock.Any(), "102").Return(nil)
	controller := Controller{ActionExecutor: executor, ModePolicy: ModePolicy{DefaultMode: cluster.JobModeAct, AllowAct: true}}

	if err := controller.reconcileGrowJobs(context.Background(), snapshot, nil); !errors.Is(err, cancelErr) {
		t.Fatalf("error = %v, want the cancellation failure", err)
	}
}

func withJob(job cluster.JobExecutionRecord, change func(*cluster.JobExecutionRecord)) *cluster.JobExecutionRecord {
	change(&job)
	return &job
}
