// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/cluster"
	"github.com/google/go-cmp/cmp"
)

// The marker is the only link from a grow job to the job attempt it grew, so
// anything that is not exactly a marker must not make a job look like one:
// SAC cancels grow jobs whose attempt stopped running.
func TestActiveJobsIdentifyGrowJobsByCommentMarker(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		want    *cluster.JobAttemptRef
	}{
		{name: "grow job", comment: "prrte-grow-of=4.0", want: &cluster.JobAttemptRef{JobID: "4", AttemptIndex: 0}},
		{name: "grow job of a requeued attempt", comment: "prrte-grow-of=4.2", want: &cluster.JobAttemptRef{JobID: "4", AttemptIndex: 2}},
		{name: "recovery spec", comment: `{"auto_resume_mode":"requeue"}`},
		{name: "no comment"},
		{name: "marker inside other text", comment: "note prrte-grow-of=4.0"},
		{name: "missing attempt", comment: "prrte-grow-of=4"},
		{name: "non-numeric job", comment: "prrte-grow-of=abc.0"},
		{name: "negative attempt", comment: "prrte-grow-of=4.-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"jobs": []SlurmJob{{JobID: 7, UserName: "alice", Comment: tt.comment}}})
			if err != nil {
				t.Fatal(err)
			}
			executor := &recordingExecutor{results: map[string][]byte{"squeue\x00--json": payload}}
			scheduler := &slurmCluster{roCommands: executor}

			jobs, err := scheduler.GetJobs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 {
				t.Fatalf("jobs = %#v, want one", jobs)
			}
			if diff := cmp.Diff(tt.want, jobs[0].GrowOf); diff != "" {
				t.Fatalf("GrowOf mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
