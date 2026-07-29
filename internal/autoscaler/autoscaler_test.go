package autoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/provisioner"
)

type mockProvisioner struct {
	instances       []provisioner.Instance
	scaleUpCalled   int
	scaleDownCalled int
}

func (m *mockProvisioner) ScaleUp(ctx context.Context, pool string, n int, labels map[string]string) ([]provisioner.InstanceID, error) {
	m.scaleUpCalled += n
	for i := 0; i < n; i++ {
		m.instances = append(m.instances, provisioner.Instance{
			ID:   provisioner.InstanceID(fmt.Sprintf("inst-%d", len(m.instances))),
			Pool: pool,
		})
	}
	return nil, nil
}

func (m *mockProvisioner) ScaleDown(ctx context.Context, ids []provisioner.InstanceID) error {
	m.scaleDownCalled += len(ids)
	return nil
}

func (m *mockProvisioner) ListInstances(ctx context.Context) ([]provisioner.Instance, error) {
	return m.instances, nil
}

func TestAutoscaler_HotPoolFloor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[]"))
	})
	mux.HandleFunc("/api/v1/queue/depth", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count": 0}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	prov := &mockProvisioner{
		instances: []provisioner.Instance{},
	}

	cfg := Config{
		HotPoolSize:  2,
		MaxBurstSize: 5,
		SchedulerURL: ts.URL,
	}

	as := New(cfg, prov)
	err := as.tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if prov.scaleUpCalled != 2 {
		t.Errorf("expected 2 scale-up calls, got %d", prov.scaleUpCalled)
	}
}

func TestAutoscaler_BurstScaleUp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		agents := []api.AgentInfo{
			{ID: "agent-1", Connected: true, Concurrency: 1, ActiveJobsCount: 1},
		}
		json.NewEncoder(w).Encode(agents)
	})
	mux.HandleFunc("/api/v1/queue/depth", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count": 2}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	prov := &mockProvisioner{
		instances: []provisioner.Instance{
			{ID: "agent-1", Pool: "hot"},
		},
	}

	cfg := Config{
		HotPoolSize:  1,
		MaxBurstSize: 5,
		SchedulerURL: ts.URL,
	}

	as := New(cfg, prov)
	err := as.tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Queue depth 2, available 0. Needs 2 burst instances.
	if prov.scaleUpCalled != 2 {
		t.Errorf("expected 2 scale-up calls, got %d", prov.scaleUpCalled)
	}
}

func TestAutoscaler_IdleTeardownSafety(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		agents := []api.AgentInfo{
			{ID: "burst-1", Connected: true, Concurrency: 1, ActiveJobsCount: 0},
		}
		json.NewEncoder(w).Encode(agents)
	})
	mux.HandleFunc("/api/v1/queue/depth", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count": 0}`))
	})
	mux.HandleFunc("/api/v1/agents/burst-1/drain", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	prov := &mockProvisioner{
		instances: []provisioner.Instance{
			{ID: "burst-1", Pool: "burst"},
		},
	}

	cfg := Config{
		HotPoolSize:  0,
		MaxBurstSize: 5,
		IdleTimeout:  -1 * time.Hour, // Always idle
		SchedulerURL: ts.URL,
	}

	as := New(cfg, prov)

	// First tick to start idle timer
	err := as.tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Second tick to trigger teardown
	err = as.tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	as.mu.Lock()
	if !as.tearingDown["burst-1"] {
		t.Errorf("expected tearingDown to be true")
	}
	as.mu.Unlock()

	// Third tick should not launch another teardown (would log if it did, but we check tearingDown state)
	err = as.tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	as.mu.Lock()
	if !as.tearingDown["burst-1"] {
		t.Errorf("expected tearingDown to still be true")
	}
	as.mu.Unlock()
}
