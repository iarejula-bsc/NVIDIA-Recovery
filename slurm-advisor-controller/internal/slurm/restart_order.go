// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/cluster"
)

// restartOrderPrefix starts the token that carries a restart-in-allocation
// order in a job's AdminComment:
//
//	nvr-restart-in-allocation:<generation>:<attempt>:<issued unix seconds>:<quarantined nodes>
//
// The quarantined nodes are comma-separated and may be empty. AdminComment is
// writable only by Slurm operators, so a job owner cannot forge an order, and
// the job reads it back with scontrol. Other content of the field belongs to
// the site and is kept; tokens are separated by spaces.
const restartOrderPrefix = "nvr-restart-in-allocation:"

// restartOrderSignal wakes the job's batch script to carry out an order. The
// script traps it; see the operator guide for the job side of the contract.
const restartOrderSignal = "USR2"

func formatRestartOrder(order cluster.InPlaceRestartOrder) string {
	return restartOrderPrefix + strings.Join([]string{
		strconv.FormatInt(order.Generation, 10),
		strconv.FormatInt(int64(order.AttemptIndex), 10),
		strconv.FormatInt(order.IssuedAt.Unix(), 10),
		strings.Join(normalizeNodes(order.Quarantine), ","),
	}, ":")
}

// restartOrderFromAdminComment returns the order an AdminComment carries, or
// nil when it carries no well-formed one.
func restartOrderFromAdminComment(adminComment string) *cluster.InPlaceRestartOrder {
	for _, token := range strings.Fields(adminComment) {
		value, ok := strings.CutPrefix(token, restartOrderPrefix)
		if !ok {
			continue
		}
		fields := strings.Split(value, ":")
		if len(fields) != 4 {
			return nil
		}
		generation, genErr := strconv.ParseInt(fields[0], 10, 64)
		attempt, attemptErr := strconv.ParseInt(fields[1], 10, 32)
		issued, issuedErr := strconv.ParseInt(fields[2], 10, 64)
		if genErr != nil || attemptErr != nil || issuedErr != nil || generation < 1 || attempt < 0 {
			return nil
		}
		order := &cluster.InPlaceRestartOrder{
			Generation:   generation,
			AttemptIndex: int32(attempt),
			IssuedAt:     time.Unix(issued, 0).UTC(),
		}
		if fields[3] != "" {
			order.Quarantine = strings.Split(fields[3], ",")
		}
		return order
	}
	return nil
}

// withRestartOrder replaces the order token in an AdminComment, keeping the
// rest of the field.
func withRestartOrder(adminComment string, order cluster.InPlaceRestartOrder) string {
	tokens := []string{}
	for _, token := range strings.Fields(adminComment) {
		if !strings.HasPrefix(token, restartOrderPrefix) {
			tokens = append(tokens, token)
		}
	}
	return strings.Join(append(tokens, formatRestartOrder(order)), " ")
}

// JobOrderRestartInAllocation records the order in the job's AdminComment and
// signals its batch script. The field is read fresh so a site's own content
// written since the last observation is kept. Writing first means a job woken
// by the signal always finds the order; a lost signal leaves the order
// unanswered, which the caller's deadline turns into a requeue.
func (s *slurmCluster) JobOrderRestartInAllocation(ctx context.Context, jobID string, order cluster.InPlaceRestartOrder) error {
	output, err := s.execute(ctx, s.roCommands, "squeue", "-j", jobID, s.squeueJSONArg())
	if err != nil {
		return err
	}
	var current struct {
		Jobs []struct {
			AdminComment string `json:"admin_comment"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(output.stdout, &current); err != nil {
		return errors.Wrapf(err, "read admin comment of job %s", jobID)
	}
	if len(current.Jobs) != 1 {
		return errors.Newf("job %s is not in the queue", jobID)
	}

	adminComment := withRestartOrder(current.Jobs[0].AdminComment, order)
	if _, err := s.execute(ctx, s.rwCommands, "scontrol", "update", "JobId="+jobID, "AdminComment="+adminComment); err != nil {
		return err
	}
	_, err = s.execute(ctx, s.rwCommands, "scancel", "--batch", "--signal="+restartOrderSignal, jobID)
	return err
}
