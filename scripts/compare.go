// Package main provides a simple benchmark comparison tool for the adaptive pool.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
)

type Stats struct {
	Completed  string
	Rejected   string
	PeakRAM    string
	PeakGoro   string
	AvgLatency string
}

func parseOutput(output string) Stats {
	s := Stats{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	var lastLogLine string

	for scanner.Scan() {
		line := scanner.Text()
		// Capture periodic log lines for fallback
		if strings.HasPrefix(line, "[") && strings.Contains(line, "Goro:") {
			lastLogLine = line
		}

		switch {
		case strings.Contains(line, "Total Tasks Completed:"):
			s.Completed = strings.TrimSpace(strings.Split(line, ":")[1])
		case strings.Contains(line, "Total Tasks Rejected:"):
			s.Rejected = strings.TrimSpace(strings.Split(line, ":")[1])
		case strings.Contains(line, "Peak RAM Usage:"):
			s.PeakRAM = strings.TrimSpace(strings.Split(line, ":")[1])
		case strings.Contains(line, "Peak Goroutines:"):
			s.PeakGoro = strings.TrimSpace(strings.Split(line, ":")[1])
		case strings.Contains(line, "Average Latency:"):
			s.AvgLatency = strings.TrimSpace(strings.Split(line, ":")[1])
		default:
			log.Printf("unhandled line: %q", line)
		}
	}

	// Fallback: If no summary (crashed), parse the last periodic log line
	if s.Completed == "" && lastLogLine != "" {
		parts := strings.Split(lastLogLine, "|")
		for _, p := range parts {
			switch {
			case strings.Contains(p, "Goro:"):
				s.PeakGoro = strings.TrimSpace(strings.Split(p, ":")[1]) + " (Crashed)"
			case strings.Contains(p, "RAM:"):
				s.PeakRAM = strings.TrimSpace(strings.Split(p, ":")[1])
			case strings.Contains(p, "Completed:"):
				s.Completed = strings.TrimSpace(strings.Split(p, ":")[1])
			case strings.Contains(p, "Latency:"):
				s.AvgLatency = strings.TrimSpace(strings.Split(p, ":")[1])
			default:
				log.Printf("unhandled periodic log line: %q", p)
			}
		}
		s.Rejected = "0 (No Protection)"
	}
	return s
}

func runSim(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var out bytes.Buffer
	mw := io.MultiWriter(&out, os.Stdout)
	c.Stdout = mw
	c.Stderr = mw
	err := c.Run()
	return out.String(), err
}

func main() {
	fmt.Println("Starting Benchmark Comparison...")
	fmt.Println("\n--- RUNNING NAIVE SIMULATION ---")
	naiveOut, _ := runSim("go", "run", "examples/one_million_simulator/naive/main.go")
	naiveStats := parseOutput(naiveOut)

	fmt.Println("\n--- RUNNING ADAPTIVE POOL SIMULATION ---")
	poolOut, _ := runSim("go", "run", "examples/one_million_simulator/with_pool/main.go")
	poolStats := parseOutput(poolOut)

	fmt.Println("\n" + "================================================================")
	fmt.Println("                  BENCHMARK COMPARISON RESULTS                  ")
	fmt.Println("================================================================")
	fmt.Printf("%-20s | %-15s | %-15s\n", "Metric", "Naive (NP)", "Adaptive Pool")
	fmt.Println("----------------------------------------------------------------")
	fmt.Printf("%-20s | %-15s | %-15s\n", "Completed Jobs", naiveStats.Completed, poolStats.Completed)
	fmt.Printf("%-20s | %-15s | %-15s\n", "Rejected Jobs", naiveStats.Rejected, poolStats.Rejected)
	fmt.Printf("%-20s | %-15s | %-15s\n", "Peak RAM Usage", naiveStats.PeakRAM, poolStats.PeakRAM)
	fmt.Printf("%-20s | %-15s | %-15s\n", "Peak Goroutines", naiveStats.PeakGoro, poolStats.PeakGoro)
	fmt.Printf("%-20s | %-15s | %-15s\n", "Avg Latency", naiveStats.AvgLatency, poolStats.AvgLatency)
	fmt.Println("================================================================")

	goroRatio := 0.0
	var nG, pG float64

	clean := strings.ReplaceAll(naiveStats.PeakGoro, " (Crashed)", "")
	if _, err := fmt.Sscanf(clean, "%f", &nG); err != nil {
		log.Printf("failed to parse naive PeakGoro %q: %v", naiveStats.PeakGoro, err)
		nG = 0 // or some fallback
	}

	if _, err := fmt.Sscanf(poolStats.PeakGoro, "%f", &pG); err != nil {
		log.Printf("failed to parse pool PeakGoro %q: %v", poolStats.PeakGoro, err)
		pG = 0 // or some fallback
	}

	if pG > 0 {
		goroRatio = nG / pG
	}

	if goroRatio > 0 {
		fmt.Printf("\nInsight: Adaptive Pool used %.1fx fewer goroutines to maintain stability.\n", goroRatio)
	}
}
