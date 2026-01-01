// Package main provides an HTTP server example using the adaptive pool.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
)

var pool adaptivepool.Pool

func main() {
	// creating the adaptive pool
	var err error
	pool, err = adaptivepool.New(
		adaptivepool.WithMinWorkers(4),
		adaptivepool.WithMaxWorkers(32),
		adaptivepool.WithQueueSize(1000),
		adaptivepool.WithScaleUpThreshold(0.7),
		adaptivepool.WithScaleDownIdleDuration(30*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to Create pool: %v", err)
	}

	// Set up HTTP routes
	http.HandleFunc("/Submit", handleSubmit)
	http.HandleFunc("/metrics", handleMetrics)
	http.HandleFunc("/health", handleHealth)

	// Start server
	server := &http.Server{
		Addr:              ":8080",
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan

		log.Println("Shutting down server...")

		// Shutdown HTTP server
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}

		// Shutdown pool
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := pool.Shutdown(ctx); err != nil {
			log.Printf("Pool shutdown error: %v", err)
		}

		log.Println("Shutdown complete")
		os.Exit(0)
	}()

	log.Printf("Server starting on %s", server.Addr)
	log.Printf("Try: curl http://localhost:8080/Submit -d '{\"task\":\"process-data\",\"duration\":100}'")
	log.Printf("Metrics: curl http://localhost:8080/metrics")

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

type JobRequest struct {
	Task     string `json:"task"`
	Duration int    `json:"duration"` // milliseconds
}

func handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req JobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// creating a job
	job := func(ctx context.Context) error {
		// Simulate work
		select {
		case <-time.After(time.Duration(req.Duration) * time.Millisecond):
			log.Printf("Completed task: %s (took %dms)", req.Task, req.Duration)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Submit with timeout
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := pool.Submit(ctx, job); err != nil {
		if err == adaptivepool.ErrPoolShutdown {
			http.Error(w, "Service is shutting down", http.StatusServiceUnavailable)
			return
		}
		if err == adaptivepool.ErrTimeout || err == context.DeadlineExceeded {
			http.Error(w, "Service overloaded, please retry", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to Submit job: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "accepted",
		"task":   req.Task,
	})
}

func handleMetrics(w http.ResponseWriter, _ *http.Request) {
	metrics := pool.Metrics()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"queue_depth":     metrics.QueueDepth(),
		"active_workers":  metrics.ActiveWorkers(),
		"jobs_processed":  metrics.JobsProcessed(),
		"jobs_rejected":   metrics.JobsRejected(),
		"avg_job_latency": metrics.AvgJobLatency().String(),
	})
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
	})
}
