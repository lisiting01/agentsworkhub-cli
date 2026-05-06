package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCycleResponseShape pins the wire contract for the cycle-mutating
// endpoints. The platform returns { job, cycle }; an earlier CLI bug
// unmarshalled the envelope directly into JobCycle which silently produced
// "Cycle #0" output. Locking the shape down here prevents regressing.
func TestCycleResponseShape(t *testing.T) {
	body := []byte(`{
		"job":   {"_id": "j1", "title": "T", "status": "active"},
		"cycle": {"_id": "c1", "cycleNumber": 7, "status": "submitted"}
	}`)
	var resp CycleResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Job == nil || resp.Cycle == nil {
		t.Fatalf("expected both job and cycle populated, got %+v", resp)
	}
	if resp.Cycle.CycleNumber != 7 {
		t.Errorf("CycleNumber = %d, want 7", resp.Cycle.CycleNumber)
	}
	if resp.Job.Title != "T" {
		t.Errorf("Job.Title = %q, want %q", resp.Job.Title, "T")
	}
}

// TestTransactionUsesDescription guards against the historical bug where
// the CLI Transaction struct read `note` (a name that was never used by the
// platform) and consequently always rendered an empty description column.
func TestTransactionUsesDescription(t *testing.T) {
	body := []byte(`{
		"_id":         "tx1",
		"type":        "pool_deposit",
		"modelId":     "claude-sonnet-4-6",
		"amount":      -100,
		"balance":     900,
		"description": "Deposit 100 tokens to job pool"
	}`)
	var tx Transaction
	if err := json.Unmarshal(body, &tx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tx.Description != "Deposit 100 tokens to job pool" {
		t.Errorf("Description = %q, expected Deposit 100 tokens to job pool", tx.Description)
	}
	if tx.Amount != -100 {
		t.Errorf("Amount = %d, expected -100 (signed amounts must round-trip)", tx.Amount)
	}
	if tx.Balance != 900 {
		t.Errorf("Balance = %d, expected 900", tx.Balance)
	}
}

// TestJobLifecycleFields verifies the new lifecycle/timeline fields and
// requirements/input/output round-trip — these were missing from the CLI
// struct so `awh jobs view` couldn't display them even when present.
func TestJobLifecycleFields(t *testing.T) {
	body := []byte(`{
		"_id": "j1",
		"title": "Build a thing",
		"requirements": "Go 1.21+",
		"input": "OpenAPI spec",
		"output": "REST API",
		"status": "submitted",
		"submittedAt": "2026-04-01T12:00:00Z",
		"acceptedAt":  "2026-03-30T08:00:00Z"
	}`)
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if job.Requirements != "Go 1.21+" || job.Input != "OpenAPI spec" || job.Output != "REST API" {
		t.Errorf("requirements/input/output not deserialized: %+v", job)
	}
	if job.SubmittedAt == nil || job.AcceptedAt == nil {
		t.Fatalf("submittedAt/acceptedAt should be populated, got %+v / %+v", job.SubmittedAt, job.AcceptedAt)
	}
}

// TestMessageAttachmentObjectShape covers the populated-attachment shape the
// platform actually returns (objects with originalName/mimeType/size), not
// raw ObjectId strings.
func TestMessageAttachmentObjectShape(t *testing.T) {
	body := []byte(`{
		"_id": "m1",
		"type": "delivery",
		"senderName": "alice",
		"attachments": [
			{"_id":"a1","originalName":"deliverable.pdf","mimeType":"application/pdf","size":42},
			{"_id":"a2","originalName":"notes.md","size":10}
		]
	}`)
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(m.Attachments))
	}
	if m.Attachments[0].OriginalName != "deliverable.pdf" {
		t.Errorf("OriginalName = %q", m.Attachments[0].OriginalName)
	}
	if m.Attachments[0].Size != 42 {
		t.Errorf("Size = %d", m.Attachments[0].Size)
	}
}

// TestListJobsSendsSkillFilter ensures the new --skill flag actually reaches
// the wire. We don't care about the response body here — just the URL.
func TestListJobsSendsSkillFilter(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"jobs":[],"total":0,"page":1,"totalPages":0}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "agent-name", "tok")
	if _, err := c.ListJobs("open", "", "", "go", 1, 20); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(capturedQuery, "skill=go") {
		t.Errorf("expected skill=go in query, got %q", capturedQuery)
	}
}

// TestAPIErrorPrefersErrorOverMessage matches the precedence the CLI relies
// on when surfacing platform errors to users.
func TestAPIErrorPrefersErrorOverMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"job is in_progress","message":"ignore me"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "n", "t")
	_, err := c.GetJob("xyz")
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Message, "job is in_progress") {
		t.Errorf("Message = %q", apiErr.Message)
	}
}
