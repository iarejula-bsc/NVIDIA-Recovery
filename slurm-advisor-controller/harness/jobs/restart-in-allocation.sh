#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Job side of a restart-in-allocation order for the harness, which runs no
# PRRTE. in-allocation-job.sh runs it when the controller signals the batch
# script. It keeps the contract the controller relies on and grows the job the
# way PRRTE does on Slurm, so the controller is exercised against real
# scheduler state:
#
# - it reads the order the controller recorded in the job's AdminComment;
# - it is idempotent: a job already grown for this attempt is left alone;
# - it gives up after --timeout seconds when no node is granted;
# - the grow is a separate "prrte" job, marked with the attempt it serves.
#
# Quarantine stops a node's runtime daemon; with no runtime there is nothing to
# stop, so the ordered nodes are only reported.

set -euo pipefail

timeout=10
while [[ $# -gt 0 ]]; do
  case "$1" in
    --timeout) timeout=$2; shift 2 ;;
    *) echo "usage: $0 [--timeout s]" >&2; exit 2 ;;
  esac
done

job=${SLURM_JOB_ID:?must run inside the job}
details=$(scontrol show job "$job")
order=$(grep -o 'nvr-restart-in-allocation:[^ ]*' <<<"$details" | head -1 || true)
if [[ -z "$order" ]]; then
  echo "job ${job} has no restart order" >&2
  exit 1
fi
IFS=: read -r _ generation attempt _ quarantine <<<"$order"
echo "job ${job}: restart order for generation ${generation}, attempt ${attempt}, quarantine '${quarantine}'"

marker="prrte-grow-of=${job}.${attempt}"
if squeue -h --name=prrte --states=RUNNING -o '%k' | grep -qxF "$marker"; then
  echo "job ${job} already grown for attempt ${attempt}"
  exit 0
fi

# Allocate outside this job's environment, as PRRTE clears the parent's
# allocation variables before it extends.
partition=$(squeue -h -j "$job" -o '%P')
if ! out=$(env -i PATH="$PATH" salloc --no-shell --exclusive --job-name=prrte \
    --partition="$partition" --nodes=1 --immediate="$timeout" 2>&1); then
  echo "no node granted within ${timeout}s: ${out}" >&2
  exit 1
fi
grow=$(sed -n 's/.*Granted job allocation \([0-9]*\).*/\1/p' <<<"$out")
scontrol update JobId="$grow" Comment="$marker"
echo "job ${job} grown by job ${grow}"
