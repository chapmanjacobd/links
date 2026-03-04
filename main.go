package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	_ "github.com/mattn/go-sqlite3"
)

type AddCmd struct {
	DBPath      string   `help:"Database path" default:"links.db" type:"path"`
	Category    string   `help:"Category" short:"c"`
	NoExtract   bool     `help:"Do not extract links from the provided URLs" short:"n"`
	PageKey     string   `help:"Page key" default:"page"`
	PageStart   int      `help:"Start page" default:"0"`
	MaxPages    int      `help:"Max pages to fetch" default:"1"`
	PageStep    int      `help:"Page step" default:"1"`
	PageReplace string   `help:"Page replace variable"`
	Paths       []string `arg:"" help:"URLs to add" optional:""`
}

type OpenCmd struct {
	DBPath         string   `help:"Database path" default:"links.db" type:"path"`
	Category       string   `help:"Filter by category" short:"c"`
	Limit          int      `help:"Limit number of links to open" default:"1" short:"L"`
	MaxSameDomain  int      `help:"Limit to N tabs per domain" short:"m"`
	RegexSort      []string `help:"Regex sort patterns" short:"r"`
	Search         []string `arg:"" help:"Search terms" optional:""`
}

var CLI struct {
	Debug bool `help:"Enable debug mode."`

	Add  AddCmd  `cmd:"" help:"Add links to the database."`
	Open OpenCmd `cmd:"" help:"Open links from the database."`
}

func initDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS media (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT NOT NULL,
			hostname TEXT,
			category TEXT,
			time_created INTEGER DEFAULT (strftime('%s', 'now')),
			time_deleted INTEGER DEFAULT 0
		);
		CREATE UNIQUE INDEX IF NOT EXISTS media_path_idx ON media (path);

		CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL,
			time_played INTEGER DEFAULT (strftime('%s', 'now')),
			FOREIGN KEY(media_id) REFERENCES media(id)
		);
	`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func (a *AddCmd) Run() error {
	db, err := initDB(a.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	inputPaths := a.Paths
	if len(inputPaths) == 0 {
		// Read from stdin
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			var stdinContent strings.Builder
			_, _ = io.Copy(&stdinContent, os.Stdin)
			inputPaths = strings.Fields(stdinContent.String())
		}
	}

	for _, p := range inputPaths {
		for i := 0; i < a.MaxPages; i++ {
			pageNum := a.PageStart + (i * a.PageStep)
			currentURL := p
			if a.MaxPages > 1 || a.PageStart != 0 || a.PageReplace != "" {
				currentURL = setPage(p, a.PageKey, pageNum, a.PageReplace)
			}

			if a.NoExtract {
				err = addLink(db, currentURL, a.Category)
				if err != nil {
					log.Printf("Error adding link %s: %v", currentURL, err)
				}
			} else {
				links, err := extractLinks(currentURL)
				if err != nil {
					log.Printf("Error extracting links from %s: %v. Adding link itself.", currentURL, err)
					err = addLink(db, currentURL, a.Category)
					if err != nil {
						log.Printf("Error adding link %s: %v", currentURL, err)
					}
					continue
				}
				for _, link := range links {
					err = addLink(db, link, a.Category)
					if err != nil {
						// Ignore duplicate errors
					}
				}
				fmt.Printf("Added %d links from %s\n", len(links), currentURL)
			}
		}
	}

	return nil
}

func setPage(inputURL, pageKey string, pageNum int, pageReplace string) string {
	if pageReplace != "" {
		return strings.ReplaceAll(inputURL, pageReplace, fmt.Sprintf("%d", pageNum))
	}

	u, err := url.Parse(inputURL)
	if err != nil {
		return inputURL
	}

	q := u.Query()
	q.Set(pageKey, fmt.Sprintf("%d", pageNum))
	u.RawQuery = q.Encode()

	return u.String()
}

func addLink(db *sql.DB, link, category string) error {
	u, err := url.Parse(link)
	hostname := ""
	if err == nil {
		hostname = u.Hostname()
	}

	_, err = db.Exec(`
		INSERT INTO media (path, hostname, category, time_created)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			category = COALESCE(NULLIF(?, ''), category)
	`, link, hostname, category, time.Now().Unix(), category)
	return err
}

var linkRegex = regexp.MustCompile(`(?i)href=["'](https?://[^"']+)["']`)

func extractLinks(pageURL string) ([]string, error) {
	resp, err := http.Get(pageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	matches := linkRegex.FindAllStringSubmatch(string(body), -1)
	links := make([]string, 0, len(matches))
	seen := make(map[string]bool)

	for _, m := range matches {
		link := m[1]
		if !seen[link] {
			links = append(links, link)
			seen[link] = true
		}
	}

	return links, nil
}

type Media struct {
	ID       int
	Path     string
	Hostname string
	Category string
}

func (o *OpenCmd) Run() error {
	db, err := initDB(o.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	query := "SELECT id, path, hostname, COALESCE(category, '') FROM media WHERE time_deleted = 0 AND id NOT IN (SELECT media_id FROM history)"
	args := []any{}
	if o.Category != "" {
		query += " AND category = ?"
		args = append(args, o.Category)
	}
	query += " ORDER BY time_created DESC"

	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var allMedia []Media
	for rows.Next() {
		var m Media
		if err := rows.Scan(&m.ID, &m.Path, &m.Hostname, &m.Category); err != nil {
			return err
		}
		allMedia = append(allMedia, m)
	}

	filtered := filterMedia(allMedia, o.Search)

	if len(o.RegexSort) > 0 {
		filtered = regexSort(filtered, o.RegexSort)
	}

	if o.MaxSameDomain > 0 {
		filtered = filterMaxSameDomain(filtered, o.MaxSameDomain)
	}

	if len(filtered) > o.Limit {
		filtered = filtered[:o.Limit]
	}

	if len(filtered) == 0 {
		fmt.Println("No links found")
		return nil
	}

	for _, m := range filtered {
		fmt.Printf("Opening: %s\n", m.Path)
		if err := openBrowser(m.Path); err != nil {
			log.Printf("Error opening browser: %v", err)
		}
		_, _ = db.Exec("INSERT INTO history (media_id, time_played) VALUES (?, ?)", m.ID, time.Now().Unix())
	}

	return nil
}

func filterMaxSameDomain(media []Media, max int) []Media {
	counts := make(map[string]int)
	var filtered []Media
	for _, m := range media {
		domain := m.Hostname
		if domain == "" {
			u, err := url.Parse(m.Path)
			if err == nil {
				domain = u.Hostname()
			}
		}
		if counts[domain] < max {
			filtered = append(filtered, m)
			counts[domain]++
		}
	}
	return filtered
}

func regexSort(media []Media, patterns []string) []Media {
	var regexs []*regexp.Regexp
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			log.Printf("Invalid regex %s: %v", p, err)
			continue
		}
		regexs = append(regexs, re)
	}

	type mediaWithKey struct {
		m   Media
		key string
	}

	keyed := make([]mediaWithKey, len(media))
	for i, m := range media {
		var keys []string
		for _, re := range regexs {
			matches := re.FindAllString(m.Path, -1)
			keys = append(keys, strings.Join(matches, ""))
		}
		keyed[i] = mediaWithKey{m, strings.Join(keys, "|")}
	}

	sort.SliceStable(keyed, func(i, j int) bool {
		return keyed[i].key < keyed[j].key
	})

	result := make([]Media, len(media))
	for i, k := range keyed {
		result[i] = k.m
	}
	return result
}

func filterMedia(media []Media, search []string) []Media {
	if len(search) == 0 {
		return media
	}

	var filtered []Media
	for _, m := range media {
		matches := true
		fullText := strings.ToLower(m.Path + " " + m.Hostname + " " + m.Category)
		for _, s := range search {
			if !strings.Contains(fullText, strings.ToLower(s)) {
				matches = false
				break
			}
		}
		if matches {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	default: // linux, freebsd, openbsd, netbsd
		cmd = "xdg-open"
		args = []string{url}
	}
	return exec.Command(cmd, args...).Start()
}

func main() {
	ctx := kong.Parse(&CLI)
	err := ctx.Run()
	if err != nil {
		log.Fatal(err)
	}
}
