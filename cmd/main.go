package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"domain-collector/internal/config"
	"domain-collector/internal/parser"
	"domain-collector/internal/storage"
)

func main() {
	domainsFile := flag.String("domains", "", "path to domains file (required)")
	workers := flag.Int("workers", 5, "number of concurrent workers")
	delay := flag.Int("delay", 1000, "delay between requests in milliseconds")
	retry := flag.Int("retry", 3, "number of retry attempts")
	dbPath := flag.String("db", "domains.db", "path to SQLite database")
	logDir := flag.String("log-dir", "logs", "path to log directory")
	clean := flag.Bool("clean", false, "clean database and logs before starting")

	flag.Parse()

	if *clean {
		log.Println("Cleaning database and logs...")
		if err := os.Remove(*dbPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: failed to remove database: %v", err)
		}
		if err := os.MkdirAll(*logDir, 0755); err == nil {
			entries, err := os.ReadDir(*logDir)
			if err == nil {
				for _, e := range entries {
					if err := os.RemoveAll(filepath.Join(*logDir, e.Name())); err != nil {
						log.Printf("Warning: failed to remove log %s: %v", e.Name(), err)
					}
				}
			}
		}
		log.Println("Cleanup completed")
	}

	if err := os.MkdirAll(*logDir, 0755); err != nil {
		log.Fatalf("Failed to create log directory: %v", err)
	}

	logFile := filepath.Join(*logDir, fmt.Sprintf("collector_%s.log", filepath.Base(*domainsFile)))
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)

	cfg := &config.Config{
		Workers: *workers,
		Delay:   *delay,
		Retry:   *retry,
		DBPath:  *dbPath,
		LogDir:  *logDir,
	}

	log.Printf("Starting domain collector with config: workers=%d, delay=%dms, retry=%d", cfg.Workers, cfg.Delay, cfg.Retry)
	storage.GlobalLogBuffer.Add("info", fmt.Sprintf("Starting domain collector: workers=%d, delay=%dms, retry=%d", cfg.Workers, cfg.Delay, cfg.Retry))

	db, err := storage.New(*dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	go func() {
		http.HandleFunc("/api/domains", db.HandleDomains)
		http.HandleFunc("/api/stats", db.HandleStats)
		http.HandleFunc("/api/logs", storage.HandleLogsStream)
		http.HandleFunc("/api/startinfo", storage.HandleStartInfo)
		http.Handle("/", http.FileServer(http.Dir("web")))
		log.Printf("Starting web server on port 1439")
		if err := http.ListenAndServe(":1439", nil); err != nil {
			log.Printf("Web server error: %v", err)
		}
	}()

	domains, err := readDomains(*domainsFile)
	if err != nil {
		log.Fatalf("Failed to read domains file: %v", err)
	}

	log.Printf("Loaded %d domains from file", len(domains))
	storage.GlobalLogBuffer.Add("info", fmt.Sprintf("Loaded %d domains from file", len(domains)))
	storage.GlobalStartInfo.Set(len(domains))

	p := parser.New(cfg)

	results := p.ParseDomains(domains)

	successCount := 0
	for result := range results {
		if err := db.SaveDomain(result); err != nil {
			log.Printf("Failed to save domain %s: %v", result.Domain, err)
			storage.GlobalLogBuffer.Add("error", fmt.Sprintf("Failed to save: %s - %v", result.Domain, err))
			continue
		}
		successCount++
		storage.GlobalStartInfo.SetProcessed(successCount)
		msg := fmt.Sprintf("Processed: %s (%s)", result.Domain, result.Status)
		log.Printf(msg)
		if result.Status == "success" {
			storage.GlobalLogBuffer.Add("success", msg)
		} else {
			storage.GlobalLogBuffer.Add("error", msg+" - "+result.ErrorMessage)
		}
	}

	log.Printf("Completed. Processed %d/%d domains. Web server running on :1439", successCount, len(domains))
	storage.GlobalLogBuffer.Add("info", fmt.Sprintf("Completed: %d/%d domains", successCount, len(domains)))

	select {}
}

func readDomains(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var domains []string
	for _, line := range splitLines(string(data)) {
		line = trimWhitespace(line)
		if line == "" || hasPrefix(line, "#") {
			continue
		}
		domains = append(domains, line)
	}
	return domains, nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimWhitespace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
