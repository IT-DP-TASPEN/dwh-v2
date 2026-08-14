package ingestionexec

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
)

func TestSourceWideFailureClassification(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusNotFound} {
		if !sourceWide(&fincloud.Error{Kind: fincloud.ErrorUpstream, HTTPStatus: status}) {
			t.Fatalf("HTTP %d was not source-wide fatal", status)
		}
	}
	for _, kind := range []fincloud.ErrorKind{fincloud.ErrorAuthentication, fincloud.ErrorUnauthorized, fincloud.ErrorMalformed} {
		if !sourceWide(&fincloud.Error{Kind: kind}) {
			t.Fatalf("%s was not source-wide fatal", kind)
		}
	}
	if sourceWide(context.Canceled) {
		t.Fatal("context cancellation classified as source failure")
	}
}

func TestJakartaSnapshotDateUsesExecutionDate(t *testing.T) {
	if got := jakartaSnapshotDate(time.Date(2026, 8, 13, 16, 59, 59, 0, time.UTC)).String(); got != "2026-08-13" {
		t.Fatalf("snapshot before Jakarta midnight=%s", got)
	}
	if got := jakartaSnapshotDate(time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC)).String(); got != "2026-08-14" {
		t.Fatalf("snapshot after Jakarta midnight=%s", got)
	}
}
