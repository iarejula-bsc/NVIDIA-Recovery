// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

/* prrte_restart: restart a Slurm job inside its allocation through its PRRTE DVM.
 *
 * The job runs it from its batch script when NVIDIA Recovery orders a restart
 * in allocation (see docs/operators/slurm.md):
 *
 *   prrte_restart [--timeout s] [--quarantine n1,n2]
 *
 * It (1) quarantines the blamed nodes that are still in the DVM and (2) grows
 * the DVM back to the size it started with. The batch script relaunches the
 * application once the DVM is whole. Both steps look at the DVM first, so a
 * repeated order does nothing more.
 *
 * Quarantine stops the node's PRRTE daemon with TERM; the daemon kills its
 * application processes first. PRRTE treats the node as lost, while the node
 * stays in the Slurm job, so Slurm does not grant it to the grow.
 *
 * The DVM is the `prte` process whose environment carries this process's
 * SLURM_JOB_ID; the size it started with is that environment's
 * SLURM_JOB_NUM_NODES. A positional <nodes> overrides the size.
 *
 * PRRTE answers a grow once Slurm has granted the nodes. --timeout bounds the
 * wait (default 60 s), after which the command fails.
 *
 * A granted grow is a Slurm job of its own. It is marked with the attempt it
 * serves, Comment=prrte-grow-of=<job ID>.<restart count>, which is how NVIDIA
 * Recovery observes the restart and cancels the grow job once that attempt
 * stops running.
 *
 * usage: prrte_restart [--count] [--timeout s] [--quarantine nodes] [<nodes>]
 * exit: 0 the DVM is whole; 1 it could not be made whole; 2 usage or no DVM */
#include <dirent.h>
#include <pmix_tool.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define MAX_HOSTS 1024

/* Seconds on a monotonic clock, for the step timings printed below. */
static double now(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec + ts.tv_nsec / 1e9;
}

static volatile int dvm_event = 0;  /* 1 ready, -1 failed */
static volatile int grow_answer = 0; /* 1 granted, -1 refused */
static char grow_job[32] = "";       /* Slurm job ID of the granted grow */

static void growcb(pmix_status_t status, pmix_info_t *info, size_t ninfo, void *cbdata,
                   pmix_release_cbfunc_t release_fn, void *release_cbdata) {
    (void)cbdata;
    for (size_t i = 0; i < ninfo; i++)
        if (PMIx_Check_key(info[i].key, PMIX_ALLOC_ID) && info[i].value.type == PMIX_STRING)
            snprintf(grow_job, sizeof grow_job, "%s", info[i].value.data.string);
    grow_answer = status == PMIX_SUCCESS || status == PMIX_OPERATION_IN_PROGRESS ? 1 : -1;
    if (release_fn) release_fn(release_cbdata);
}

static void evhandler(size_t id, pmix_status_t status, const pmix_proc_t *src,
                      pmix_info_t info[], size_t ninfo, pmix_info_t *results, size_t nresults,
                      pmix_event_notification_cbfunc_fn_t cbfunc, void *cbdata) {
    (void)id; (void)src; (void)info; (void)ninfo; (void)results; (void)nresults;
    dvm_event = status == PMIX_DVM_IS_READY ? 1 : -1;
    if (cbfunc) cbfunc(PMIX_EVENT_ACTION_COMPLETE, NULL, 0, NULL, NULL, cbdata);
}

/* Returns the pid of the process named comm whose SLURM_JOB_ID is jobid, or 0.
 * When size and restarts are not NULL they receive that process's
 * SLURM_JOB_NUM_NODES and SLURM_RESTART_COUNT (unset on the first run, so 0).
 * Only processes of this user are considered, unless this is root. */
static pid_t find_job_process(const char *comm_want, const char *jobid, int *size, int *restarts) {
    DIR *proc = opendir("/proc");
    struct dirent *e;
    pid_t found = 0;
    char want[64];
    snprintf(want, sizeof want, "SLURM_JOB_ID=%s", jobid);
    while (proc && (e = readdir(proc)) && !found) {
        pid_t pid = (pid_t)atoi(e->d_name);
        char path[64], comm[32] = "";
        struct stat st;
        if (pid <= 0) continue;
        snprintf(path, sizeof path, "/proc/%d", pid);
        if (stat(path, &st) || (getuid() != 0 && st.st_uid != getuid())) continue;
        snprintf(path, sizeof path, "/proc/%d/comm", pid);
        FILE *f = fopen(path, "r");
        if (!f) continue;
        if (!fgets(comm, sizeof comm, f)) comm[0] = 0;
        fclose(f);
        comm[strcspn(comm, "\n")] = 0;
        if (strcmp(comm, comm_want)) continue;
        snprintf(path, sizeof path, "/proc/%d/environ", pid);
        if (!(f = fopen(path, "r"))) continue;
        char var[4096];
        size_t n = 0;
        int c, match = 0, nodes = 0, restart = 0;
        while ((c = fgetc(f)) != EOF) {
            if (c && n < sizeof var - 1) { var[n++] = (char)c; continue; }
            var[n] = 0;
            if (!strcmp(var, want)) match = 1;
            if (!strncmp(var, "SLURM_JOB_NUM_NODES=", 20)) nodes = atoi(var + 20);
            if (!strncmp(var, "SLURM_RESTART_COUNT=", 20)) restart = atoi(var + 20);
            n = 0;
        }
        fclose(f);
        if (match) {
            found = pid;
            if (size) *size = nodes;
            if (restarts) *restarts = restart;
        }
    }
    if (proc) closedir(proc);
    return found;
}

/* Collects the distinct PMIX_HOSTNAME strings under info into hosts[]. */
static void collect_hosts(const pmix_info_t *info, size_t n, char hosts[][64], int *nhosts) {
    for (size_t i = 0; i < n; i++) {
        if (PMIx_Check_key(info[i].key, PMIX_HOSTNAME) && info[i].value.type == PMIX_STRING) {
            int known = 0;
            for (int k = 0; k < *nhosts; k++) known |= !strcmp(hosts[k], info[i].value.data.string);
            if (!known && *nhosts < MAX_HOSTS) snprintf(hosts[(*nhosts)++], 64, "%s", info[i].value.data.string);
        }
        if (info[i].value.type == PMIX_DATA_ARRAY && info[i].value.data.darray &&
            info[i].value.data.darray->type == PMIX_INFO)
            collect_hosts(info[i].value.data.darray->array, info[i].value.data.darray->size, hosts, nhosts);
    }
}

/* Fills hosts[] with the live nodes of the DVM and returns how many, or -1.
 * PRRTE leaves lost and released nodes out of the allocation. */
static int live_nodes(char hosts[][64]) {
    pmix_query_t *q;
    pmix_status_t rc;
    PMIX_QUERY_CREATE(q, 1);
    PMIX_ARGV_APPEND(rc, q[0].keys, PMIX_QUERY_ALLOCATION);
    pmix_info_t *res = NULL;
    size_t nres = 0;
    rc = PMIx_Query_info(q, 1, &res, &nres);
    if (rc != PMIX_SUCCESS) { printf("allocation query: %s\n", PMIx_Error_string(rc)); return -1; }
    int nhosts = 0;
    collect_hosts(res, nres, hosts, &nhosts);
    return nhosts;
}

static int is_live(const char *node, char hosts[][64], int nhosts) {
    for (int i = 0; i < nhosts; i++)
        if (!strcmp(hosts[i], node)) return 1;
    return 0;
}

/* Stops this job's PRRTE daemon on the local node. Runs as a step on the
 * quarantined node; see quarantine(). */
static int stop_local_daemon(void) {
    const char *jobid = getenv("SLURM_JOB_ID");
    pid_t pid = jobid ? find_job_process("prted", jobid, NULL, NULL) : 0;
    if (!pid) return 0; /* already gone */
    return kill(pid, SIGTERM) ? 1 : 0;
}

/* Runs argv and returns 0 when it exits with 0. */
static int run(char *const argv[]) {
    pid_t child = fork();
    if (child == 0) {
        execvp(argv[0], argv);
        _exit(127);
    }
    int status = 0;
    if (child < 0 || waitpid(child, &status, 0) < 0) return 1;
    return WIFEXITED(status) && WEXITSTATUS(status) == 0 ? 0 : 1;
}

/* Runs this program with --stop-local-daemon as a one-task step on node. */
static int quarantine(const char *node) {
    char self[4096];
    ssize_t n = readlink("/proc/self/exe", self, sizeof self - 1);
    if (n <= 0) return 1;
    self[n] = 0;
    char nodelist[128];
    snprintf(nodelist, sizeof nodelist, "--nodelist=%s", node);
    char *argv[] = {"srun", "--overlap", "--nodes=1", "--ntasks=1", nodelist, self, "--stop-local-daemon", NULL};
    return run(argv);
}

/* Marks the grow job with the job attempt it serves (see the header). */
static int mark_grow_job(const char *job, const char *jobid, int restarts) {
    char jobarg[64], comment[96];
    snprintf(jobarg, sizeof jobarg, "JobId=%s", job);
    snprintf(comment, sizeof comment, "Comment=prrte-grow-of=%s.%d", jobid, restarts);
    char *argv[] = {"scontrol", "update", jobarg, comment, NULL};
    return run(argv);
}

int main(int argc, char **argv) {
    static char hosts[MAX_HOSTS][64];
    char *blamed = NULL;
    int count_only = 0, timeout = 60, i = 1;
    for (; i < argc && argv[i][0] == '-'; i++) {
        if (!strcmp(argv[i], "--stop-local-daemon")) return stop_local_daemon();
        else if (!strcmp(argv[i], "--count")) count_only = 1;
        else if (!strcmp(argv[i], "--quarantine") && i + 1 < argc) blamed = argv[++i];
        else if (!strcmp(argv[i], "--timeout") && i + 1 < argc) timeout = atoi(argv[++i]);
        else {
            fprintf(stderr, "usage: %s [--count] [--timeout s] [--quarantine nodes] [<nodes>]\n", argv[0]);
            return 2;
        }
    }
    if (i < argc - 1) {
        fprintf(stderr, "usage: %s [--count] [--timeout s] [--quarantine nodes] [<nodes>]\n", argv[0]);
        return 2;
    }

    const char *jobid = getenv("SLURM_JOB_ID");
    int want = 0, restarts = 0;
    pid_t pid = jobid ? find_job_process("prte", jobid, &want, &restarts) : 0;
    if (!pid) { fprintf(stderr, "no PRRTE DVM of job %s on this node\n", jobid ? jobid : "(unset)"); return 2; }
    if (i < argc) want = atoi(argv[i]);
    if (want <= 0 && !count_only) { fprintf(stderr, "cannot tell the size the DVM started with\n"); return 2; }

    pmix_info_t init[1];
    PMIX_INFO_LOAD(&init[0], PMIX_SERVER_PIDINFO, &pid, PMIX_PID);
    pmix_proc_t me;
    pmix_status_t rc = PMIx_tool_init(&me, init, 1);
    if (rc != PMIX_SUCCESS) { printf("PMIx_tool_init: %s\n", PMIx_Error_string(rc)); return 2; }
    pmix_status_t codes[2] = {PMIX_DVM_IS_READY, PMIX_ERR_DVM_MOD};
    PMIx_Register_event_handler(codes, 2, NULL, 0, evhandler, NULL, NULL);
    double t0 = now();

    int have = live_nodes(hosts);
    for (int k = 0; k < have; k++) printf("  live node: %s\n", hosts[k]);
    if (count_only || have < 0) {
        PMIx_tool_finalize();
        return have < 0 ? 1 : 0;
    }

    /* 1. Quarantine the blamed nodes still in the DVM, then wait until PRRTE
     *    has taken them out, so the count below does not include them. */
    int ok = 1;
    for (char *node = blamed ? strtok(blamed, ",") : NULL; node; node = strtok(NULL, ",")) {
        if (!is_live(node, hosts, have)) continue;
        int stopped = quarantine(node) == 0;
        for (int s = 0; stopped && s < 300; s++) {
            have = live_nodes(hosts);
            if (have < 0 || !is_live(node, hosts, have)) break;
            usleep(100000);
        }
        printf("[%.1f s] quarantine %s: %s\n", now() - t0, node,
               stopped && have >= 0 && !is_live(node, hosts, have) ? "done" : "failed");
        ok &= stopped && have >= 0 && !is_live(node, hosts, have);
    }
    have = ok ? live_nodes(hosts) : -1;
    printf("DVM has %d live node(s), %d wanted\n", have, want);
    fflush(stdout);
    if (!ok || have < 0 || have >= want) {
        PMIx_tool_finalize();
        return ok && have >= want ? 0 : 1;
    }

    /* 2. Grow by the missing nodes only, within the deadline. */
    uint64_t missing = (uint64_t)(want - have);
    pmix_info_t req[1];
    PMIX_INFO_LOAD(&req[0], PMIX_ALLOC_NUM_NODES, &missing, PMIX_UINT64);
    dvm_event = 0;
    rc = PMIx_Allocation_request_nb(PMIX_ALLOC_EXTEND, req, 1, growcb, NULL);
    time_t deadline = time(NULL) + timeout;
    while (rc == PMIX_SUCCESS && grow_answer == 0 && time(NULL) < deadline) usleep(100000);
    printf("[%.1f s] extend by %lu node(s): %s\n", now() - t0, (unsigned long)missing,
           rc != PMIX_SUCCESS ? PMIx_Error_string(rc)
           : grow_answer == 1 ? "granted" : grow_answer == -1 ? "refused" : "no node granted in time");
    fflush(stdout);
    ok = rc == PMIX_SUCCESS && grow_answer == 1;
    /* Mark before waiting for the DVM: the nodes are held from the grant on. A
     * mark that fails leaves the grow job to its time limit if this job ends
     * uncleanly; the restart itself does not depend on it. */
    if (ok) {
        int marked = grow_job[0] && mark_grow_job(grow_job, jobid, restarts) == 0;
        printf("[%.1f s] grow job %s %s\n", now() - t0, grow_job[0] ? grow_job : "(unknown)",
               marked ? "marked" : "NOT marked");
    }
    if (ok) {
        while (dvm_event == 0 && time(NULL) < deadline) usleep(100000);
        printf("[%.1f s] DVM event: %s\n", now() - t0,
               dvm_event == 1 ? "DVM_IS_READY" : dvm_event == -1 ? "ERR_DVM_MOD" : "none (timeout)");
        ok = dvm_event == 1 && live_nodes(hosts) >= want;
    }
    PMIx_tool_finalize();
    return ok ? 0 : 1;
}
