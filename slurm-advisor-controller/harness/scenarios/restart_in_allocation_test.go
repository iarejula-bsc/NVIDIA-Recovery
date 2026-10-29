// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build harness

package scenarios

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// inAllocationJob carries out restart orders as a PRRTE job would.
const inAllocationJob = "/jobs/in-allocation-job.sh"

// inAllocationComment declares PRRTE in-place recovery for a managed job.
func inAllocationComment() string {
	raw, err := json.Marshal(map[string]map[string]string{"APS": {"in_place_recovery": "prrte"}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// startControllerWithRestartInAllocation starts an acting controller that
// orders restarts in the allocation and waits briefly for them, so a job that
// cannot obtain a node falls back to a requeue within the scenario.
func (h harness) startControllerWithRestartInAllocation() {
	h.t.Helper()
	h.startControllerWithEnv(
		controllerModeAct,
		"SLURM_ADVISOR_RESTART_IN_ALLOCATION_ENABLED=true",
		"SLURM_ADVISOR_RESTART_IN_ALLOCATION_TIMEOUT=20s",
	)
}

// waitForGrowJob waits for a running grow job marked for the job attempt and
// returns its job ID.
func (h harness) waitForGrowJob(jobID string, attempt int) string {
	h.t.Helper()
	marker := fmt.Sprintf("prrte-grow-of=%s.%d", jobID, attempt)
	var growJobID string
	h.wait(func() (bool, string) {
		out := h.compose("exec", "-T", "slurmctld", "squeue", "-h", "--name=prrte", "--states=RUNNING", "-o", "%A %k")
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[1] == marker {
				growJobID = fields[0]
				return true, line
			}
		}
		return false, out
	})
	return growJobID
}

type adviceStatusSummary struct {
	AdviceID    string `json:"advice_id"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

// waitForAdviceDisposition waits for the controller to report the Advice's
// terminal disposition to the backend.
func (h harness) waitForAdviceDisposition(adviceID, disposition string) {
	h.t.Helper()
	h.wait(func() (bool, string) {
		statuses, err := h.adviceStatuses()
		if err != nil {
			return false, err.Error()
		}
		for _, status := range statuses {
			if status.AdviceID == adviceID {
				return status.Disposition == disposition, fmt.Sprintf("%+v", status)
			}
		}
		return false, fmt.Sprintf("no status for %s in %+v", adviceID, statuses)
	})
}

func (h harness) adviceStatuses() ([]adviceStatusSummary, error) {
	resp, err := h.httpClient.Get(h.debugURL + "/advice-statuses")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /advice-statuses: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out []adviceStatusSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
