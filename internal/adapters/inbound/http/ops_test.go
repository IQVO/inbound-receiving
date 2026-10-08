package http_test

import (
	"net/http"
	"testing"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
)

func TestDocksListIsEmptyAndPermissiveByDefault(t *testing.T) {
	f := newFixture(t)
	r := f.get(t, "/docks")
	items, ok := r.body["items"].([]any)
	if r.status != http.StatusOK || str(r, "mode") != "permissive" || !ok || len(items) != 0 {
		t.Fatalf("docks = %d %s", r.status, r.raw)
	}
}

func TestDocksListsTheLocalCopyInKafkaMode(t *testing.T) {
	f := newKafkaModeFixture(t)
	_ = f.doors.Upsert(t.Context(), repository.DockDoor{Code: "WH1-DOCK-IO-02", Flow: repository.DockFlowBoth})
	_ = f.doors.Upsert(t.Context(), repository.DockDoor{Code: "WH1-DOCK-IN-01", Flow: repository.DockFlowInbound})
	r := f.get(t, "/docks")
	items, _ := r.body["items"].([]any)
	first, _ := items[0].(map[string]any)
	if str(r, "mode") != "kafka" || len(items) != 2 || first["doorCode"] != "WH1-DOCK-IN-01" || first["dockFlow"] != "Inbound" {
		t.Fatalf("docks = %s", r.raw)
	}
}

func TestDocksInternalError(t *testing.T) {
	f := newFixture(t)
	f.faults.docksList = errDB
	expectProblem(t, f.get(t, "/docks"), http.StatusInternalServerError, "internal-error")
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	f := newFixture(t)
	if r := f.get(t, "/healthz"); r.status != http.StatusOK || str(r, "status") != "ok" {
		t.Fatalf("healthz = %d %s", r.status, r.raw)
	}
	if r := f.get(t, "/readyz"); r.status != http.StatusOK || str(r, "status") != "ready" {
		t.Fatalf("readyz = %d %s", r.status, r.raw)
	}
	if r := f.get(t, "/metrics"); r.status != http.StatusOK || r.raw != "# metrics\n" {
		t.Fatalf("metrics = %d %q", r.status, r.raw)
	}
	f.readiness.SetNotReady()
	if r := f.get(t, "/readyz"); r.status != http.StatusServiceUnavailable || str(r, "status") != "not_ready" {
		t.Fatalf("draining readyz = %d %s", r.status, r.raw)
	}
	if r := f.get(t, "/healthz"); r.status != http.StatusOK {
		t.Fatalf("liveness must survive draining: %d", r.status)
	}
}

func TestReadinessNilIsReady(t *testing.T) {
	var g *inboundhttp.Readiness
	g.SetNotReady()
	if !g.Ready() {
		t.Fatal("a nil gate is ready")
	}
}

func TestUnknownRoutesAre404(t *testing.T) {
	f := newFixture(t)
	if r := f.get(t, "/nope"); r.status != http.StatusNotFound {
		t.Fatalf("status = %d", r.status)
	}
}
