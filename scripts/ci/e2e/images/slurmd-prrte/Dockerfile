# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Slinky slurmd with a PRRTE runtime that can restart a job inside its
# allocation, for the opt-in restart-in-allocation E2E run
# (E2E_RESTART_IN_ALLOCATION).
#
# Work in progress: PRRTE needs fixes that are not in an upstream release yet.
# The image builds PMIx and PRRTE master at pinned commits and applies:
#   0001 state: count a node left empty as gone
#   0002 odls: skip dead daemons when slicing cpusets
#   0003 dt: pack only mapped processes
#   0004 errmgr: mark a node down when its daemon is lost
#   0005 plm/slurm: do not pass SLURM_STEPMGR to the srun of a grow
#   0006 query: leave released nodes out of the allocation
#   0007 plm/slurm: let the daemon srun exit after a daemon loss
# The srun wrapper drops --external-launcher, which makes Slurm end the whole
# daemon step when one daemon ends despite --no-kill.
ARG SLURMD_IMAGE=ghcr.io/slinkyproject/slurmd:25.05-ubuntu24.04

FROM ubuntu:24.04 AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        autoconf automake build-essential ca-certificates flex git libevent-dev \
        libhwloc-dev libjansson-dev libtool patch perl pkg-config python3 zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*
ENV PREFIX=/opt/prrte
ARG PMIX_COMMIT=dba155a57a47a93b4c8e15e61d021b8dfce3cfce
RUN git init /src/openpmix && cd /src/openpmix \
    && git remote add origin https://github.com/openpmix/openpmix.git \
    && git fetch --depth 1 origin "$PMIX_COMMIT" && git checkout FETCH_HEAD \
    && git submodule update --init --recursive --depth 1 \
    && ./autogen.pl && ./configure --prefix="$PREFIX" --disable-debug \
    && make -j"$(nproc)" install
ARG PRRTE_COMMIT=a783dab5948c76bca5bde1647f291d9492f33fca
COPY patches /src/patches
RUN git init /src/prrte && cd /src/prrte \
    && git remote add origin https://github.com/openpmix/prrte.git \
    && git fetch --depth 1 origin "$PRRTE_COMMIT" && git checkout FETCH_HEAD \
    && git submodule update --init --recursive --depth 1 \
    && for p in /src/patches/*.patch; do patch -p1 < "$p"; done \
    && ./autogen.pl && ./configure --prefix="$PREFIX" --disable-debug --with-jansson \
                                   --enable-slurm-extensions --with-pmix="$PREFIX" \
    && make -j"$(nproc)" install
COPY prrte_restart.c /src/
RUN gcc -Wall -O2 -o "$PREFIX/bin/prrte_restart" /src/prrte_restart.c \
        -I"$PREFIX/include" -L"$PREFIX/lib" -lpmix -Wl,-rpath,"$PREFIX/lib"

FROM ${SLURMD_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends \
        libevent-2.1-7t64 libevent-core-2.1-7t64 libevent-pthreads-2.1-7t64 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /opt/prrte /opt/prrte
COPY srun /opt/prrte/srun-wrapper/srun
RUN chmod 0755 /opt/prrte/srun-wrapper/srun \
    && printf '/opt/prrte/lib\n' > /etc/ld.so.conf.d/prrte.conf && ldconfig
