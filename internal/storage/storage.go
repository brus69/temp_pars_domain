package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"domain-collector/internal/parser"

	_ "github.com/mattn/go-sqlite3"
)

type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type LogBuffer struct {
	mu      sync.RWMutex
	logs    []LogEntry
	maxSize int
}

func NewLogBuffer(maxSize int) *LogBuffer {
	return &LogBuffer{
		logs:    make([]LogEntry, 0, maxSize),
		maxSize: maxSize,
	}
}

func (lb *LogBuffer) Add(level, message string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.logs = append(lb.logs, LogEntry{
		Time:    time.Now().Format("02.01.2006 15:04:05"),
		Level:   level,
		Message: message,
	})
	if len(lb.logs) > lb.maxSize {
		lb.logs = lb.logs[len(lb.logs)-lb.maxSize:]
	}
}

func (lb *LogBuffer) GetAll() []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	result := make([]LogEntry, len(lb.logs))
	copy(result, lb.logs)
	return result
}

var GlobalLogBuffer = NewLogBuffer(1000)

type StartInfo struct {
	StartTime        time.Time `json:"startTime"`
	TotalDomains     int       `json:"totalDomains"`
	ProcessedDomains int       `json:"processedDomains"`
	mu               sync.RWMutex
}

func (si *StartInfo) Set(totalDomains int) {
	si.mu.Lock()
	defer si.mu.Unlock()
	si.StartTime = time.Now()
	si.TotalDomains = totalDomains
}

func (si *StartInfo) SetProcessed(processed int) {
	si.mu.Lock()
	defer si.mu.Unlock()
	si.ProcessedDomains = processed
}

func (si *StartInfo) Get() (time.Time, int, int) {
	si.mu.RLock()
	defer si.mu.RUnlock()
	return si.StartTime, si.TotalDomains, si.ProcessedDomains
}

var GlobalStartInfo = &StartInfo{}

type DB struct {
	db *sql.DB
}

func New(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) Migrate() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS domains (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			domain TEXT NOT NULL UNIQUE,
			url TEXT,
			title TEXT,
			description TEXT,
			h1 TEXT,
			text_content TEXT,
			phones TEXT,
			emails TEXT,
			inn TEXT,
			social_media TEXT,
			address TEXT,
			status TEXT NOT NULL,
			error_message TEXT,
			scraped_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_domain ON domains(domain);
		CREATE INDEX IF NOT EXISTS idx_status ON domains(status);
	`)
	return err
}

func (d *DB) SaveDomain(r *parser.Result) error {
	_, err := d.db.Exec(`
		INSERT OR REPLACE INTO domains 
		(domain, url, title, description, h1, text_content, phones, emails, inn, social_media, address, status, error_message, scraped_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		r.Domain,
		r.URL,
		r.Title,
		r.Description,
		r.H1,
		r.Text,
		r.Phones,
		r.Emails,
		r.INN,
		r.SocialMedia,
		r.Address,
		r.Status,
		r.ErrorMessage,
		r.ScrapedAt.Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetAllDomains() ([]parser.Result, error) {
	rows, err := d.db.Query(`
		SELECT domain, url, title, description, h1, text_content, phones, emails, inn, social_media, address, status, error_message, scraped_at
		FROM domains ORDER BY id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []parser.Result
	for rows.Next() {
		var r parser.Result
		var scrapedAt string
		err := rows.Scan(
			&r.Domain, &r.URL, &r.Title, &r.Description, &r.H1, &r.Text,
			&r.Phones, &r.Emails, &r.INN, &r.SocialMedia, &r.Address,
			&r.Status, &r.ErrorMessage, &scrapedAt,
		)
		if err != nil {
			return nil, err
		}
		r.ScrapedAt, _ = time.Parse(time.RFC3339, scrapedAt)
		results = append(results, r)
	}
	return results, nil
}

func (d *DB) GetDomainsPaginated(page, limit int, search, searchField, status string) ([]parser.Result, int, error) {
	offset := (page - 1) * limit

	fieldMap := map[string]string{
		"domain":      "domain",
		"title":       "title",
		"phones":      "phones",
		"emails":      "emails",
		"status":      "status",
		"address":     "address",
		"description": "description",
		"h1":          "h1",
		"inn":         "inn",
		"socialMedia": "social_media",
	}
	field := fieldMap[searchField]
	if field == "" {
		field = "domain"
	}

	conditions := []string{}
	args := []interface{}{}

	if search != "" {
		conditions = append(conditions, fmt.Sprintf("%s LIKE ?", field))
		args = append(args, "%"+search+"%")
	}

	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`SELECT domain, url, title, description, h1, text_content, phones, emails, inn, social_media, address, status, error_message, scraped_at FROM domains %s ORDER BY id DESC LIMIT ? OFFSET ?`, whereClause)
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM domains %s`, whereClause)

	queryArgs := append(args, limit, offset)
	countArgs := args

	rows, err := d.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []parser.Result
	for rows.Next() {
		var r parser.Result
		var scrapedAt string
		err := rows.Scan(
			&r.Domain, &r.URL, &r.Title, &r.Description, &r.H1, &r.Text,
			&r.Phones, &r.Emails, &r.INN, &r.SocialMedia, &r.Address,
			&r.Status, &r.ErrorMessage, &scrapedAt,
		)
		if err != nil {
			return nil, 0, err
		}
		r.ScrapedAt, _ = time.Parse(time.RFC3339, scrapedAt)
		results = append(results, r)
	}

	var total int
	err = d.db.QueryRow(countQuery, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	return results, total, nil
}

func (d *DB) GetDomainsByStatus(status string) ([]parser.Result, error) {
	rows, err := d.db.Query(`
		SELECT domain, url, title, description, h1, text_content, phones, emails, inn, social_media, address, status, error_message, scraped_at
		FROM domains WHERE status = ? ORDER BY id DESC
	`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []parser.Result
	for rows.Next() {
		var r parser.Result
		var scrapedAt string
		err := rows.Scan(
			&r.Domain, &r.URL, &r.Title, &r.Description, &r.H1, &r.Text,
			&r.Phones, &r.Emails, &r.INN, &r.SocialMedia, &r.Address,
			&r.Status, &r.ErrorMessage, &scrapedAt,
		)
		if err != nil {
			return nil, err
		}
		r.ScrapedAt, _ = time.Parse(time.RFC3339, scrapedAt)
		results = append(results, r)
	}
	return results, nil
}

func (d *DB) GetStats() (map[string]int, error) {
	rows, err := d.db.Query(`
		SELECT status, COUNT(*) as count FROM domains GROUP BY status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		stats[status] = count
	}
	return stats, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) HandleDomains(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.URL.Path != "/api/domains" {
		http.NotFound(w, r)
		return
	}

	page := 1
	limit := 1000
	search := ""
	searchField := "domain"
	status := ""

	if p := r.URL.Query().Get("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	search = r.URL.Query().Get("search")
	if s := r.URL.Query().Get("searchField"); s != "" {
		searchField = s
	}
	status = r.URL.Query().Get("status")

	domains, total, err := d.GetDomainsPaginated(page, limit, search, searchField, status)
	if err != nil {
		http.Error(w, fmt.Sprintf("error: %v", err), 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data":  domains,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

func (d *DB) HandleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.URL.Path != "/api/stats" {
		http.NotFound(w, r)
		return
	}

	stats, err := d.GetStats()
	if err != nil {
		http.Error(w, fmt.Sprintf("error: %v", err), 500)
		return
	}
	json.NewEncoder(w).Encode(stats)
}

func HandleLogsStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	clientGone := r.Context().Done()

	initialLogs := GlobalLogBuffer.GetAll()
	for _, log := range initialLogs {
		data, _ := json.Marshal(log)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	notify := make(chan struct{})
	go func() {
		<-clientGone
		close(notify)
	}()

	for {
		select {
		case <-notify:
			return
		default:
			time.Sleep(500 * time.Millisecond)
			logs := GlobalLogBuffer.GetAll()
			if len(logs) > 0 {
				data, _ := json.Marshal(logs[len(logs)-1])
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

func HandleStartInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	startTime, totalDomains, processedDomains := GlobalStartInfo.Get()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"startTime":        startTime.Format("02.01.2006 15:04:05"),
		"totalDomains":     totalDomains,
		"processedDomains": processedDomains,
	})
}
