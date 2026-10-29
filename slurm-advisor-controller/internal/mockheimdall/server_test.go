// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mockheimdall_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	pb "github.com/dsx-ai-factory/nvidia-recovery/api-gateway/gen/go/heimdall/v1"
	"github.com/dsx-ai-factory/nvidia-recovery/slurm-advisor-controller/internal/mockheimdall"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestServerStoresCreateOrUpdateRequests(t *testing.T) {
	ctx := context.Background()
	server := mockheimdall.NewServer(nil)

	_, err := server.CreateOrUpdateJob(ctx, &pb.CreateOrUpdateJobRequest{
		JobId:        "12345",
		AttemptIndex: 2,
		State:        pb.JobStatus_JOB_STATUS_RUNNING,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateJob() failed: %v", err)
	}

	upserts := server.Upserts()
	if len(upserts) != 1 {
		t.Fatalf("len(upserts) = %d, want 1", len(upserts))
	}
	if upserts[0].GetJobId() != "12345" || upserts[0].GetAttemptIndex() != 2 {
		t.Fatalf("upsert = %#v, want 12345 attempt 2", upserts[0])
	}
}

func TestServerListsAdviceWithPagination(t *testing.T) {
	ctx := context.Background()
	server := mockheimdall.NewServer([]*pb.Advice{
		{HeimdallJobId: "advice-1"},
		{HeimdallJobId: "advice-2"},
		{HeimdallJobId: "advice-3"},
	})

	first, err := server.ListAdvice(ctx, &pb.ListAdviceRequest{Limit: 2})
	if err != nil {
		t.Fatalf("ListAdvice(first) failed: %v", err)
	}
	if len(first.GetAdvice()) != 2 || first.GetAdvice()[0].GetHeimdallJobId() != "advice-1" {
		t.Fatalf("first page = %v, want advice-1/advice-2", first.GetAdvice())
	}
	if first.GetNextPageToken() != "2" {
		t.Fatalf("first next page token = %q, want 2", first.GetNextPageToken())
	}

	second, err := server.ListAdvice(ctx, &pb.ListAdviceRequest{Page: first.NextPageToken, Limit: 2})
	if err != nil {
		t.Fatalf("ListAdvice(second) failed: %v", err)
	}
	if len(second.GetAdvice()) != 1 || second.GetAdvice()[0].GetHeimdallJobId() != "advice-3" {
		t.Fatalf("second page = %v, want advice-3", second.GetAdvice())
	}
	if second.GetNextPageToken() != "" {
		t.Fatalf("second next page token = %q, want empty", second.GetNextPageToken())
	}
}

func TestDebugAdviceEndpointReplacesAdvice(t *testing.T) {
	ctx := context.Background()
	server := mockheimdall.NewServer(nil)
	mux := http.NewServeMux()
	server.RegisterDebugHandlers(mux)

	body := strings.NewReader(`[
		{
			"advice_id": "restart-12345",
			"job_id": "12345",
			"attempt_index": 1,
			"state": "ADVICE_STATE_RESTARTED",
			"scheduler_metadata": {
				"scheduler": "slurm",
				"managed_by": "slurm-advisor-controller"
			}
		}
	]`)
	req := httptest.NewRequest(http.MethodPut, "/advice", body)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT /advice status = %d body = %q, want 204", rec.Code, rec.Body.String())
	}
	resp, err := server.ListAdvice(ctx, &pb.ListAdviceRequest{Limit: 10})
	if err != nil {
		t.Fatalf("ListAdvice() failed: %v", err)
	}
	if len(resp.GetAdvice()) != 1 {
		t.Fatalf("len(advice) = %d, want 1", len(resp.GetAdvice()))
	}
	advice := resp.GetAdvice()[0]
	if advice.GetHeimdallJobId() != "restart-12345" || advice.GetJobId() != "12345" || advice.GetAttemptIndex() != 1 {
		t.Fatalf("advice identity = %#v, want restart-12345 job 12345 attempt 1", advice)
	}
	if advice.GetAdvice().GetState() != pb.AdviceState_ADVICE_STATE_RESTARTED {
		t.Fatalf("advice state = %s, want ADVICE_STATE_RESTARTED", advice.GetAdvice().GetState())
	}
	if advice.GetSchedulerMetadata().GetFields()["scheduler"].GetStringValue() != "slurm" {
		t.Fatalf("scheduler metadata = %v, want slurm", advice.GetSchedulerMetadata())
	}
	if advice.GetCreatedAt() == nil || advice.GetCreatedAt().CheckValid() != nil {
		t.Fatalf("created_at = %v, want valid timestamp", advice.GetCreatedAt())
	}
}

func TestDebugUpsertsEndpointIncludesSchedulerMetadata(t *testing.T) {
	ctx := context.Background()
	server := mockheimdall.NewServer(nil)
	metadata, err := structpb.NewStruct(map[string]any{
		"scheduler":            "slurm",
		"slurm_log_file_paths": []any{"/data/parent-42.out", "/data/step-42-0.out"},
	})
	if err != nil {
		t.Fatalf("build scheduler metadata: %v", err)
	}
	if _, err := server.CreateOrUpdateJob(ctx, &pb.CreateOrUpdateJobRequest{
		JobId:             "42",
		AttemptIndex:      1,
		State:             pb.JobStatus_JOB_STATUS_RUNNING,
		SchedulerMetadata: metadata,
	}); err != nil {
		t.Fatalf("CreateOrUpdateJob() failed: %v", err)
	}

	mux := http.NewServeMux()
	server.RegisterDebugHandlers(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/upserts", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /upserts status = %d body = %q, want 200", rec.Code, rec.Body.String())
	}
	var summaries []mockheimdall.UpsertSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decode GET /upserts response: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	wantPaths := []any{"/data/parent-42.out", "/data/step-42-0.out"}
	if got := summaries[0].SchedulerMetadata["slurm_log_file_paths"]; !reflect.DeepEqual(got, wantPaths) {
		t.Fatalf("scheduler metadata log paths = %#v, want %#v", got, wantPaths)
	}
}

// Controllers list only active Advice; like the backend, the mock stops
// listing an Advice once its current generation is reported terminal, so a
// harness scenario sees the same retry behavior as production.
func TestServerActiveListingDropsAdviceReconciledInCurrentGeneration(t *testing.T) {
	ctx := context.Background()
	server := mockheimdall.NewServer([]*pb.Advice{
		{HeimdallJobId: "converged"},
		{HeimdallJobId: "older-generation", Generation: 2},
		{HeimdallJobId: "in-progress"},
	})
	for adviceID, generation := range map[string]int64{"converged": 1, "older-generation": 1} {
		if _, err := server.UpdateAdviceReconciliationStatus(ctx, &pb.UpdateAdviceReconciliationStatusRequest{
			HeimdallJobId: adviceID,
			Status: &pb.AdviceReconciliationStatus{
				ObservedGeneration: generation,
				Disposition:        pb.AdviceReconciliationDisposition_ADVICE_RECONCILIATION_DISPOSITION_CONVERGED,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	onlyActive := true
	active, err := server.ListAdvice(ctx, &pb.ListAdviceRequest{OnlyActive: &onlyActive})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range active.GetAdvice() {
		got = append(got, item.GetHeimdallJobId())
	}
	if want := []string{"older-generation", "in-progress"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active advice = %v, want %v", got, want)
	}
	all, err := server.ListAdvice(ctx, &pb.ListAdviceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.GetAdvice()) != 3 {
		t.Fatalf("unfiltered advice = %v, want all three", all.GetAdvice())
	}
}
