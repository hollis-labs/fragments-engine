package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestRoutingRepository_AddAndList(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	repo := NewRoutingRepository(st.DB)
	dest, err := repo.AddDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"./corpus"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := repo.AddRoute(context.Background(), domain.Route{
		Name:          "claude-chat-route",
		MatchSource:   "claude",
		MatchType:     "chat",
		DestinationID: dest.ID,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	destinations, err := repo.ListDestinations(context.Background())
	if err != nil {
		t.Fatalf("list destinations: %v", err)
	}
	if len(destinations) != 1 {
		t.Fatalf("expected 1 destination, got %d", len(destinations))
	}
	routes, err := repo.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].ID != route.ID {
		t.Fatalf("unexpected route id: %s", routes[0].ID)
	}
}
