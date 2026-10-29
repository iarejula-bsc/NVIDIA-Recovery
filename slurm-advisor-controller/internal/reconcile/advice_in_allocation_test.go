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
	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/cluster/clustermock"
	"github.com/google/go-cmp/cmp"
	"go.uber.org/mock/gomock"
)

var inAllocationNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// prrteJob is a running job that declared PRRTE in-place recovery and still
// holds c1 and c2 after losing c3.
func prrteJob() cluster.JobExecutionRecord {
	return managedJob(cluster.JobExecutionRecord{
		JobID:          "100",
		State:          cluster.JobStateRunning,
		AllocatedNodes: []string{"c1", "c2"},
		RecoveryConfig: &cluster.RecoveryConfig{InPlaceRecovery: cluster.InPlaceRecoveryPRRTE},
	})
}

// orderedPRRTEJob is prrteJob after the order for generation 1 was given at
// issuedAt.
func orderedPRRTEJob(issuedAt time.Time) cluster.JobExecutionRecord {
	job := prrteJob()
	job.RestartOrder = &cluster.InPlaceRestartOrder{Generation: 1, AttemptIndex: 0, IssuedAt: issuedAt, Quarantine: []string{"c2"}}
	return job
}

// restartBlamingNodes is a restart Advice that excludes a node the job still
// holds (c2) and the node it already lost (c3).
func restartBlamingNodes(t *testing.T) *pb.Advice {
	advice := newAdvice(t, adviceFixture{
		adviceID:    "restart-100",
		jobID:       "100",
		state:       adviceState(pb.AdviceState_ADVICE_STATE_RESTARTED),
		metadata:    map[string]any{"scheduler": cluster.SchedulerSlurm},
		constraints: &pb.NodeConstraints{Exclusions: []string{"c3", "c2"}},
	})
	advice.Generation = 1
	return advice
}

// inAllocationSnapshot holds the restart Advice, the job, and, when grown, the
// grow job its attempt obtained by restarting in place.
func inAllocationSnapshot(advice *pb.Advice, job cluster.JobExecutionRecord, grown bool) ControllerSnapshot {
	snapshot := newControllerSnapshot(inAllocationNow)
	addAdvice(&snapshot, advice)
	addActiveJob(&snapshot, job)
	if grown {
		snapshot.GrowJobs = []cluster.JobExecutionRecord{{
			JobID: "101", State: cluster.JobStateRunning,
			GrowOf: &cluster.JobAttemptRef{JobID: job.JobID, AttemptIndex: job.AttemptIndex},
		}}
	}
	return snapshot
}

func inAllocationController(executor cluster.JobMutator, client *fakeControllerClient, audit AuditObserver) Controller {
	return Controller{
		HeimdallClient: client, ActionExecutor: executor, AuditObserver: audit,
		ModePolicy: ModePolicy{DefaultMode: cluster.JobModeAct, AllowAct: true},
		Options:    ControllerOptions{RestartInAllocation: true, RestartInAllocationTimeout: 2 * time.Minute},
		Now:        func() time.Time { return inAllocationNow },
	}
}

func expectRequeue(executor *clustermock.MockJobMutator) {
	gomock.InOrder(
		executor.EXPECT().JobRequeueHold(gomock.Any(), "100").Return(nil),
		executor.EXPECT().JobNodeUpdate(gomock.Any(), "100", []string{"c2", "c3"}, nil, 0).Return(nil),
		executor.EXPECT().JobRelease(gomock.Any(), "100").Return(nil),
	)
}

func TestRestartAdviceForPRRTEJobOrdersRestartInsideAllocation(t *testing.T) {
	executor := newMockJobMutator(t)
	// The order names the Advice generation and attempt it answers, and only
	// the blamed node the job still holds.
	want := cluster.InPlaceRestartOrder{Generation: 1, AttemptIndex: 0, IssuedAt: inAllocationNow, Quarantine: []string{"c2"}}
	executor.EXPECT().JobOrderRestartInAllocation(gomock.Any(), "100", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, order cluster.InPlaceRestartOrder) error {
			if diff := cmp.Diff(want, order); diff != "" {
				t.Errorf("order mismatch (-want +got):\n%s", diff)
			}
			return nil
		})
	client := &fakeControllerClient{}
	audit := &recordingAuditObserver{}
	controller := inAllocationController(executor, client, audit)

	if _, err := controller.reconcileAdvice(context.Background(), inAllocationSnapshot(restartBlamingNodes(t), prrteJob(), false), nil); err != nil {
		t.Fatal(err)
	}

	// Giving the order is not convergence; only observing its outcome is.
	if len(client.statusUpdates) != 0 {
		t.Fatalf("status updates = %#v, want none until the restart is observed", client.statusUpdates)
	}
	if len(audit.adviceActions) != 1 || audit.adviceActions[0].Action != SchedulerActionRestartInAllocation ||
		audit.adviceActions[0].Outcome != SchedulerActionOutcomeSuccess {
		t.Fatalf("action audit = %#v, want one successful order", audit.adviceActions)
	}
}

func TestRestartAdviceConvergesOnceOrderedAttemptHasGrown(t *testing.T) {
	client := &fakeControllerClient{}
	controller := inAllocationController(newMockJobMutator(t), client, nil)
	snapshot := inAllocationSnapshot(restartBlamingNodes(t), orderedPRRTEJob(inAllocationNow.Add(-10*time.Second)), true)

	if _, err := controller.reconcileAdvice(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}

	if len(client.statusUpdates) != 1 {
		t.Fatalf("status updates = %#v, want one convergence", client.statusUpdates)
	}
	status := client.statusUpdates[0].Status
	if status.GetDisposition() != pb.AdviceReconciliationDisposition_ADVICE_RECONCILIATION_DISPOSITION_CONVERGED ||
		status.GetReason() != pb.AdviceReconciliationReason_ADVICE_RECONCILIATION_REASON_RESTARTED_IN_PLACE {
		t.Fatalf("status = %v, want converged as restarted in place", status)
	}
}

func TestRestartAdviceWaitsForOrderedRestartWithinTimeout(t *testing.T) {
	client := &fakeControllerClient{}
	// Neither another order nor a requeue while the job carries out the order.
	controller := inAllocationController(newMockJobMutator(t), client, nil)
	snapshot := inAllocationSnapshot(restartBlamingNodes(t), orderedPRRTEJob(inAllocationNow.Add(-time.Minute)), false)

	if _, err := controller.reconcileAdvice(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if len(client.statusUpdates) != 0 {
		t.Fatalf("status updates = %#v, want none while waiting", client.statusUpdates)
	}
}

func TestRestartAdviceRequeuesWhenOrderedRestartTimesOut(t *testing.T) {
	executor := newMockJobMutator(t)
	expectRequeue(executor)
	controller := inAllocationController(executor, &fakeControllerClient{}, nil)
	snapshot := inAllocationSnapshot(restartBlamingNodes(t), orderedPRRTEJob(inAllocationNow.Add(-2*time.Minute)), false)

	if _, err := controller.reconcileAdvice(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
}

// Recovery is modeled per attempt, so an attempt is restarted inside its
// allocation at most once: a later generation finds the attempt grown with no
// order of its own, and requeues it into a fresh attempt.
func TestRestartAdviceRequeuesAnAttemptAlreadyRestartedInsideAllocation(t *testing.T) {
	executor := newMockJobMutator(t)
	expectRequeue(executor)
	controller := inAllocationController(executor, &fakeControllerClient{}, nil)
	advice := restartBlamingNodes(t)
	advice.Generation = 2

	if _, err := controller.reconcileAdvice(context.Background(), inAllocationSnapshot(advice, orderedPRRTEJob(inAllocationNow), true), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRestartAdviceRetriesFailedOrderWithoutRequeue(t *testing.T) {
	executor := newMockJobMutator(t)
	orderErr := errors.New("scontrol failed")
	executor.EXPECT().JobOrderRestartInAllocation(gomock.Any(), "100", gomock.Any()).Return(orderErr)
	controller := inAllocationController(executor, &fakeControllerClient{}, nil)

	_, err := controller.reconcileAdvice(context.Background(), inAllocationSnapshot(restartBlamingNodes(t), prrteJob(), false), nil)
	if !errors.Is(err, orderErr) {
		t.Fatalf("error = %v, want the order failure retried from fresh state", err)
	}
}

func TestRestartAdviceRequeuesJobsThatCannotRestartInsideAllocation(t *testing.T) {
	withoutDeclaration := prrteJob()
	withoutDeclaration.RecoveryConfig = nil
	notRunning := prrteJob()
	notRunning.State = cluster.JobStateFailed

	tests := []struct {
		name    string
		job     cluster.JobExecutionRecord
		enabled bool
	}{
		{name: "disabled on the controller", job: prrteJob(), enabled: false},
		{name: "job did not declare PRRTE", job: withoutDeclaration, enabled: true},
		{name: "job no longer holds its allocation", job: notRunning, enabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := newMockJobMutator(t)
			expectRequeue(executor)
			controller := inAllocationController(executor, &fakeControllerClient{}, nil)
			controller.Options.RestartInAllocation = tt.enabled

			if _, err := reconcileAdviceFixture(&controller, context.Background(), restartBlamingNodes(t), tt.job, cluster.JobModeAct, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestartAdviceObserveModePlansOnlyTheOrder(t *testing.T) {
	observation := &SyncObservation{}
	controller := Controller{Options: ControllerOptions{RestartInAllocation: true}}

	result, err := reconcileAdviceFixture(&controller, context.Background(), restartBlamingNodes(t), prrteJob(), cluster.JobModeObserve, observation)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TerminalOutcomes) != 0 {
		t.Fatalf("terminal outcomes = %#v, a plan is not convergence", result.TerminalOutcomes)
	}
	if len(observation.AdviceActions) != 1 {
		t.Fatalf("actions = %#v, want only the order", observation.AdviceActions)
	}
	assertActionObservation(t, observation.AdviceActions, ActionAdviceRestartInAllocation, cluster.JobModeObserve, ObservationOutcomePlanned, 1)
}
