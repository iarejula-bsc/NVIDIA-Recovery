# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Opt-in coverage of a restart inside the job's allocation through its PRRTE runtime.

Work in progress: it runs only with E2E_RESTART_IN_ALLOCATION, which builds the PRRTE slurmd image with fixes that are
not in an upstream PRRTE release yet.
"""

from __future__ import annotations

import uuid
from collections.abc import Callable
from pathlib import Path

import pytest
from e2e_harness import audit
from e2e_harness.checks import (
    advice_converged,
    audit_trail,
    hardware_attribution,
    job_state,
    system_log_line,
    watcher_anomaly,
)
from e2e_harness.config import E2EConfig
from e2e_harness.recovery_client import RecoveryClient
from e2e_harness.retry_policy import restart_in_allocation_enabled
from e2e_harness.workloads import WATCHER_CRASH_RECOVERY, recovery_metadata, slurm_batch
from recovery_libs.testing.fixtures import Fixtures
from recovery_libs.testing.kubernetes import StackControl
from recovery_libs.testing.launch.jobs import JobSpec, JobState
from recovery_libs.testing.scenario import Check, Evidence, Run, Scenario, eventually
from recovery_libs.testing.scenario_plugin import scenario

pytestmark = pytest.mark.exclusive

NAME = "58-restart-in-allocation"
NODES = frozenset({"prrte-a-0", "prrte-b-0"})
CRASH_MARKER = "segmentation fault"


def _victim(run: Run[RecoveryClient]) -> str | None:
    """The node the job injects the fault on: its node that does not run the batch script."""
    batch_host = run.job.metadata.get("batch_host")
    others = [node for node in run.job.nodes if node != batch_host]
    return others[0] if batch_host and len(others) == 1 else None


def on_victim(check_for: Callable[[str], Check[RecoveryClient]]) -> Check[RecoveryClient]:
    """Run the check built for the job's victim node."""

    def check(run: Run[RecoveryClient]) -> Evidence:
        victim = _victim(run)
        if victim is None:
            return Evidence(False, f"job {run.job.job_id} on {list(run.job.nodes)} has no victim node yet")
        return check_for(victim)(run)

    return check


def running_on_both_nodes() -> Check[RecoveryClient]:
    """The job runs on both PRRTE nodes it asked for, with a batch host and one other node."""

    def check(run: Run[RecoveryClient]) -> Evidence:
        held = run.job.state is JobState.RUNNING and set(run.job.nodes) == NODES and _victim(run) is not None
        return Evidence(
            held,
            f"job {run.job.job_id} is {run.job.state} on {list(run.job.nodes)}, "
            f"batch host {run.job.metadata.get('batch_host')}",
        )

    return check


@scenario
def test_restart_in_allocation_replaces_the_blamed_node(
    e2e_config: E2EConfig, fixtures: Fixtures, slurm_stack: StackControl
) -> Scenario[RecoveryClient]:
    """A hardware fault on one node of a running PRRTE job is recovered inside its allocation, without a requeue."""
    if e2e_config.profile != "advisor-act":
        pytest.skip("restart in allocation requires the action-enabled advisor profile")
    if e2e_config.attribution_mode != "available":
        pytest.skip("restart in allocation quarantines the node that Attribution blames")
    if not restart_in_allocation_enabled(slurm_stack):
        pytest.skip("restart in allocation is disabled in the deployed controller (E2E_RESTART_IN_ALLOCATION)")
    syslog_marker = f"HEIMDALL_E2E_SYSLOG_{uuid.uuid4().hex}"
    evidence_window = e2e_config.watcher_evidence_timeout_seconds
    return Scenario(
        name=NAME,
        job=JobSpec(
            name=NAME,
            workload=slurm_batch(Path(__file__).with_name("job.sh")),
            environment={
                "WORKER": fixtures.path(f"scenarios/{NAME}/worker.sh"),
                "KERNEL_LOG_PATH": fixtures.path(f"scenarios/{NAME}/kern.log"),
                "SYSLOG_INJECTOR": fixtures.path("e2e_harness/inject-syslog.sh"),
                "SYSLOG_MARKER": syslog_marker,
            },
            metadata=recovery_metadata(WATCHER_CRASH_RECOVERY, {"in_place_recovery": "prrte"}),
        ),
        stages=(
            eventually("running on its two nodes", running_on_both_nodes(), within=e2e_config.validation_timeout(600)),
            eventually(
                "syslog collected",
                on_victim(lambda victim: system_log_line(syslog_marker, node=victim)),
                within=evidence_window,
            ),
            eventually("detected", watcher_anomaly("CRASH", marker=CRASH_MARKER), within=evidence_window),
            eventually(
                "attributed",
                on_victim(
                    lambda victim: hardware_attribution(
                        "CONFIRMED", signature="XID-79-GPU_HAS_FALLEN_OFF_THE_BUS", node=victim
                    )
                ),
                within=evidence_window,
            ),
            eventually(
                "restarted in place",
                advice_converged(reason="RESTARTED_IN_PLACE"),
                job_state(JobState.RUNNING, restarts=0),
                within=e2e_config.validation_timeout(600),
            ),
            eventually(
                "completed without a requeue",
                job_state(JobState.COMPLETED, restarts=0),
                within=e2e_config.validation_timeout(900),
            ),
            eventually(
                "audited",
                audit_trail(
                    lambda job: (
                        audit.adi_fast_attribution_exchange_event(job),
                        audit.scheduler_action_event(job, action="slurm.restart_in_allocation"),
                        audit.advice_reconciliation_converged_event(job, reason="RESTARTED_IN_PLACE"),
                    )
                ),
                within=90,
            ),
        ),
    )
