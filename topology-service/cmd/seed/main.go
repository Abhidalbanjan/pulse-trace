package main

import (
	"context"
	"log"

	"github.com/pulsetrace/topology-service/internal/repository"
)

func main() {
	log.Println("Connecting to Neo4j to seed data...")
	repo, err := repository.NewNeo4j("bolt://localhost:7687", "neo4j", "pulsetrace_secret", "")
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer repo.Close(context.Background())

	ctx := context.Background()

	// Seed edges
	edges := [][]string{
		{"ingress-nginx", "api-gateway"},
		{"api-gateway", "auth-service"},
		{"api-gateway", "checkout-service"},
		{"checkout-service", "payment-service"},
		{"checkout-service", "inventory-service"},
		{"payment-service", "stripe-api"},
		{"inventory-service", "postgres-db"},
		{"auth-service", "redis-cache"},
	}

	for _, e := range edges {
		if err := repo.UpsertDependencyEdge(ctx, "default", e[0], e[1]); err != nil {
			log.Printf("Failed to insert edge %s -> %s: %v", e[0], e[1], err)
		} else {
			log.Printf("Inserted edge: %s -> %s", e[0], e[1])
		}
	}

	// Update states
	states := map[string]string{
		"payment-service":   "DEGRADED",
		"inventory-service": "HEALTHY",
		"postgres-db":       "HEALTHY",
	}

	for svc, state := range states {
		if err := repo.UpdateServiceState(ctx, "default", svc, state); err != nil {
			log.Printf("Failed to update state for %s: %v", svc, err)
		} else {
			log.Printf("Updated state for %s: %s", svc, state)
		}
	}

	log.Println("Seeding complete.")
}
