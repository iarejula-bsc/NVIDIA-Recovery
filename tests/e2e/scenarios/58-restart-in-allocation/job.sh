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

# A PRRTE DVM spans the allocation and the application is relaunched from its
# checkpoint whenever it fails. NVIDIA Recovery orders a restart in allocation
# by writing the order into the job's AdminComment and sending SIGUSR2; the
# trap carries it out with prrte_restart. After FAULT_AT seconds, the node that
# does not run this script gets an XID 79 in its syslog and its rank crashes on
# every launch until the node is quarantined.

: "${WORKER:?WORKER is required}"
: "${KERNEL_LOG_PATH:?KERNEL_LOG_PATH is required}"
: "${SYSLOG_INJECTOR:?SYSLOG_INJECTOR is required}"
: "${SYSLOG_MARKER:?SYSLOG_MARKER is required}"
: "${FAULT_AT:=30}"
: "${SYSLOG_INGESTION_DELAY_SEC:=30}"
: "${RESTART_TIMEOUT:=90}"
: "${STEPS:=180}"
export STEPS

# Never the batch host: the DVM lives there, and losing it ends the job.
victim=$(scontrol show hostnames "$SLURM_JOB_NODELIST" | grep -vx "$(hostname)" | head -1)
export PATH="/opt/prrte/srun-wrapper:/opt/prrte/bin:$PATH"
STATE_DIR="$PWD/restart-in-allocation-$SLURM_JOB_ID"
export STATE_DIR
uri="$STATE_DIR/dvm.uri"
mkdir -p "$STATE_DIR/ckpt"

log() { echo "restart-in-allocation $(date +%s.%N) $*"; }

prte --allow-run-as-root --report-uri "$uri" --prtemca prte_elastic_mode 1 &
until [[ -s "$uri" ]] && prun --allow-run-as-root --dvm-uri "file:$uri" -n 1 true 2>/dev/null; do
  sleep 1
done

# Carry out a restart order in the background, one at a time; prrte_restart
# does nothing more for an order already carried out.
carry_out_order() {
  (
    flock -n 9 || exit 0
    order=$(scontrol show job "$SLURM_JOB_ID" | grep -o 'nvr-restart-in-allocation:[^ ]*' | head -1)
    IFS=: read -r _ generation attempt _ quarantine <<<"$order"
    log "order: generation=$generation attempt=$attempt quarantine=${quarantine:-none}"
    prrte_restart --timeout "$RESTART_TIMEOUT" ${quarantine:+--quarantine "$quarantine"}
    log "order carried out: exit $?"
  ) 9>"$STATE_DIR/order.lock" &
}
trap carry_out_order USR2

(
  sleep "$FAULT_AT"
  log "fault: XID 79 on $victim"
  srun --overlap --nodes=1 --ntasks=1 --nodelist="$victim" \
    --export="ALL,KERNEL_LOG_PATH=$KERNEL_LOG_PATH,SYSLOG_MARKER=$SYSLOG_MARKER" bash "$SYSLOG_INJECTOR"
  sleep "$SYSLOG_INGESTION_DELAY_SEC"
  touch "$STATE_DIR/bad-$victim"
) &

while :; do
  prun --allow-run-as-root --dvm-uri "file:$uri" -n "$SLURM_JOB_NUM_NODES" --map-by ppr:1:node \
    -x STATE_DIR -x STEPS bash "$WORKER" &
  app=$!
  # A trap interrupts wait with a status above 128 while the application runs.
  while :; do
    wait "$app"
    rc=$?
    kill -0 "$app" 2>/dev/null || break
  done
  [[ "$rc" -eq 0 ]] && break
  log "application failed"
  sleep 5 &
  wait $!
done
log "application completed"
pterm --dvm-uri "file:$uri"
