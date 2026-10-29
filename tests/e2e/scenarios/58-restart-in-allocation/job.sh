#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#SBATCH --time=00:15:00
#SBATCH --job-name=restart-in-allocation
#SBATCH --partition=prrte
#SBATCH --nodes=2
#SBATCH --nodelist=prrte-a-0,prrte-b-0
#SBATCH --no-kill
set -uo pipefail

# The application runs under nvr-prrte-run, which restarts it inside the
# allocation when NVIDIA Recovery orders it. After FAULT_AT seconds, the node
# that does not run this script gets an XID 79 in its syslog and its rank
# crashes on every launch until the node is quarantined.

: "${WORKER:?WORKER is required}"
: "${KERNEL_LOG_PATH:?KERNEL_LOG_PATH is required}"
: "${SYSLOG_INJECTOR:?SYSLOG_INJECTOR is required}"
: "${SYSLOG_MARKER:?SYSLOG_MARKER is required}"
: "${FAULT_AT:=30}"
: "${SYSLOG_INGESTION_DELAY_SEC:=30}"
: "${STEPS:=180}"
export STEPS

export PATH="/opt/prrte/srun-wrapper:/opt/prrte/bin:$PATH"
STATE_DIR="$PWD/restart-in-allocation-$SLURM_JOB_ID"
export STATE_DIR
mkdir -p "$STATE_DIR/ckpt"
# Never the batch host: the PRRTE DVM lives there, and losing it ends the job.
victim=$(scontrol show hostnames "$SLURM_JOB_NODELIST" | grep -vx "$(hostname)" | head -1)

(
  sleep "$FAULT_AT"
  echo "fault: XID 79 on $victim"
  srun --overlap --nodes=1 --ntasks=1 --nodelist="$victim" \
    --export="ALL,KERNEL_LOG_PATH=$KERNEL_LOG_PATH,SYSLOG_MARKER=$SYSLOG_MARKER" bash "$SYSLOG_INJECTOR"
  sleep "$SYSLOG_INGESTION_DELAY_SEC"
  touch "$STATE_DIR/bad-$victim"
) &

# The restart order signals this script's process, so the wrapper replaces it.
exec nvr-prrte-run -n "$SLURM_JOB_NUM_NODES" --map-by ppr:1:node -x STATE_DIR -x STEPS bash "$WORKER"
