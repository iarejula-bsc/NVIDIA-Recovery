#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# One rank per node. Each rank advances a counter once a second and
# checkpoints it, so a relaunch resumes where the last run stopped. A rank on a
# node marked bad crashes at once, on every launch.
set -uo pipefail

rank=${PMIX_RANK:?not launched by PRRTE}
checkpoint="$STATE_DIR/ckpt/$rank"
step=$(cat "$checkpoint" 2>/dev/null || echo 0)
echo "rank $rank on $(hostname) resumes at step $step"
while (( step < STEPS )); do
  if [[ -e "$STATE_DIR/bad-$(hostname)" ]]; then
    echo "rank $rank on $(hostname): segmentation fault"
    exit 139
  fi
  sleep 1
  step=$((step + 1))
  echo "$step" > "$checkpoint"
done
echo "rank $rank on $(hostname) done at step $step"
