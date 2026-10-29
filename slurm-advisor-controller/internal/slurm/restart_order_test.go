// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/cluster"
	"github.com/google/go-cmp/cmp"
)

var issued = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// The order travels in AdminComment, which the site may also use: ordering
// keeps the site's content, replaces an earlier order, and wakes the job.
func TestJobOrderRestartInAllocationRecordsOrderAndSignalsBatchScript(t *testing.T) {
	current, err := json.Marshal(map[string]any{"jobs": []map[string]any{{
		"admin_comment": "site-ticket=42 nvr-restart-in-allocation:1:0:1793180000:c9",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	executor := &recordingExecutor{results: map[string][]byte{
		commandKey([]string{"squeue", "-j", "12345", "--json"}): current,
	}}
	adapter := &slurmCluster{rwCommands: executor, roCommands: executor}

	order := cluster.InPlaceRestartOrder{Generation: 2, AttemptIndex: 0, IssuedAt: issued, Quarantine: []string{"c4", "c2"}}
	if err := adapter.JobOrderRestartInAllocation(context.Background(), "12345", order); err != nil {
		t.Fatal(err)
	}

	assertCommandArgs(t, executor.calls, [][]string{
		{"squeue", "-j", "12345", "--json"},
		{"scontrol", "update", "JobId=12345", "AdminComment=site-ticket=42 nvr-restart-in-allocation:2:0:1793188800:c2,c4"},
		{"scancel", "--batch", "--signal=USR2", "12345"},
	})
}

// Observation reads the order back exactly; anything that is not a
// well-formed order is no order, so it can never be mistaken for one.
func TestActiveJobsReadRestartOrderFromAdminComment(t *testing.T) {
	tests := []struct {
		name         string
		adminComment string
		want         *cluster.InPlaceRestartOrder
	}{
		{
			name:         "order with quarantine beside site content",
			adminComment: "site-ticket=42 nvr-restart-in-allocation:2:1:1793188800:c4,c2",
			want:         &cluster.InPlaceRestartOrder{Generation: 2, AttemptIndex: 1, IssuedAt: issued, Quarantine: []string{"c4", "c2"}},
		},
		{
			name:         "order without quarantine",
			adminComment: "nvr-restart-in-allocation:1:0:1793188800:",
			want:         &cluster.InPlaceRestartOrder{Generation: 1, AttemptIndex: 0, IssuedAt: issued},
		},
		{name: "no admin comment"},
		{name: "site content only", adminComment: "site-ticket=42"},
		{name: "missing field", adminComment: "nvr-restart-in-allocation:1:0:1793188800"},
		{name: "generation zero", adminComment: "nvr-restart-in-allocation:0:0:1793188800:"},
		{name: "not a number", adminComment: "nvr-restart-in-allocation:x:0:1793188800:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"jobs": []SlurmJob{{JobID: 7, UserName: "alice", AdminComment: tt.adminComment}}})
			if err != nil {
				t.Fatal(err)
			}
			executor := &recordingExecutor{results: map[string][]byte{"squeue\x00--json": payload}}
			jobs, err := (&slurmCluster{roCommands: executor}).GetJobs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 {
				t.Fatalf("jobs = %#v, want one", jobs)
			}
			if diff := cmp.Diff(tt.want, jobs[0].RestartOrder); diff != "" {
				t.Fatalf("RestartOrder mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
