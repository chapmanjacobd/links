package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"mime"
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
	"golang.org/x/net/html"
)

type AddCmd struct {
	DBPath      string   `help:"Database path" default:"links.db" type:"path" aliases:"db"`
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
	DBPath        string   `help:"Database path" default:"links.db" type:"path" aliases:"db"`
	Category      string   `help:"Filter by category" short:"c"`
	Limit         int      `help:"Limit number of matching rows to process" default:"1" short:"L"`
	MaxSameDomain int      `help:"Limit to N tabs per domain" short:"m"`
	RegexSort     bool     `help:"Enable regex sort" short:"R"`
	RegexPatterns []string `help:"Custom regex patterns" short:"r"`
	All           bool     `help:"Print the total count of matching rows instead of opening links" short:"a"`
	DeleteRows    bool     `help:"Hard delete matching rows instead of opening them"`
	MarkDeleted   bool     `help:"Soft delete matching rows instead of opening them"`
	Browser       string   `help:"Browser command override"`
	NoBrowser     bool     `help:"Only print matching links; do not open them"`
	NoMarkWatched bool     `help:"Do not mark printed or opened links as seen"`
	// Intentionally no kong default tag: slice defaults would be appended to user-provided values.
	Prefix []string `help:"Prefix for non-URL paths; repeatable; defaults to https://duckduckgo.com/?q="`
	Search []string `arg:"" help:"Search terms" optional:""`
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
	link = normalizeURL(link)
	if link == "" {
		return nil
	}

	u, err := url.Parse(link)
	hostname := ""
	if err == nil {
		hostname = u.Hostname()
	}

	_, err = db.Exec(`
		INSERT INTO media (path, hostname, category, time_created, time_deleted)
		VALUES (?, ?, ?, ?, 0)
		ON CONFLICT(path) DO UPDATE SET
			hostname = excluded.hostname,
			category = COALESCE(NULLIF(excluded.category, ''), media.category),
			time_deleted = 0
	`, link, hostname, category, time.Now().Unix())
	return err
}

func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Remove trailing slash
	return strings.TrimSuffix(s, "/")
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func extractLinks(pageURL string) ([]string, error) {
	baseURL, err := url.Parse(pageURL)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Get(baseURL.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected status code: %s", resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	if !isHTMLContentType(contentType) {
		return nil, fmt.Errorf("unexpected content type: %s", contentType)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, err
	}

	baseURL = resolveBaseURL(doc, baseURL)

	var links []string
	seen := make(map[string]bool)

	walkHTML(doc, func(node *html.Node) {
		if node.Type != html.ElementNode || node.Data != "a" {
			return
		}

		href, ok := htmlAttr(node, "href")
		if !ok {
			return
		}

		link, ok := resolveLink(baseURL, href)
		if !ok || seen[link] {
			return
		}

		links = append(links, link)
		seen[link] = true
	})

	return links, nil
}

func isHTMLContentType(contentType string) bool {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return true
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return true
	}

	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func resolveBaseURL(doc *html.Node, pageURL *url.URL) *url.URL {
	baseURL := pageURL
	walkHTML(doc, func(node *html.Node) {
		if baseURL != pageURL {
			return
		}
		if node.Type != html.ElementNode || node.Data != "base" {
			return
		}

		href, ok := htmlAttr(node, "href")
		if !ok {
			return
		}

		resolved, err := pageURL.Parse(href)
		if err == nil {
			baseURL = resolved
		}
	})
	return baseURL
}

func walkHTML(node *html.Node, visit func(*html.Node)) {
	if node == nil {
		return
	}

	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func htmlAttr(node *html.Node, key string) (string, bool) {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val, true
		}
	}
	return "", false
}

func resolveLink(baseURL *url.URL, href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false
	}

	lowerHref := strings.ToLower(href)
	switch {
	case strings.HasPrefix(lowerHref, "#"),
		strings.HasPrefix(lowerHref, "javascript:"),
		strings.HasPrefix(lowerHref, "mailto:"),
		strings.HasPrefix(lowerHref, "tel:"),
		strings.HasPrefix(lowerHref, "data:"):
		return "", false
	case strings.HasPrefix(href, "//"):
		href = baseURL.Scheme + ":" + href
	}

	refURL, err := url.Parse(href)
	if err != nil {
		return "", false
	}

	resolved := baseURL.ResolveReference(refURL)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}

	resolved.Fragment = ""
	return normalizeURL(resolved.String()), true
}

type Media struct {
	ID       int
	Path     string
	Hostname string
	Category string
}

var stdout io.Writer = os.Stdout

const defaultPrefix = "https://duckduckgo.com/?q="

var startProcess = func(cmd string, args ...string) error {
	return exec.Command(cmd, args...).Start()
}

var sleep = time.Sleep

func (o *OpenCmd) Run() error {
	if o.DeleteRows && o.MarkDeleted {
		return fmt.Errorf("--delete-rows and --mark-deleted cannot be used together")
	}
	if o.All && (o.DeleteRows || o.MarkDeleted) {
		return fmt.Errorf("--all cannot be used with --delete-rows or --mark-deleted")
	}

	db, err := initDB(o.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	query := "SELECT id, path, COALESCE(hostname, ''), COALESCE(category, '') FROM media WHERE time_deleted = 0 AND id NOT IN (SELECT media_id FROM history)"
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

	if o.RegexSort || len(o.RegexPatterns) > 0 {
		filtered = regexSort(filtered, o.RegexPatterns)
	}

	if o.MaxSameDomain > 0 {
		filtered = filterMaxSameDomain(filtered, o.MaxSameDomain)
	}

	if o.All {
		fmt.Fprintln(stdout, len(filtered))
		return nil
	}

	if len(filtered) > o.Limit {
		filtered = filtered[:o.Limit]
	}

	if len(filtered) == 0 {
		fmt.Fprintln(stdout, "No links found")
		return nil
	}

	if o.MarkDeleted {
		deletedAt := time.Now().Unix()
		for _, m := range filtered {
			fmt.Fprintf(stdout, "Marking deleted: %s\n", m.Path)
			_, err = db.Exec("UPDATE media SET time_deleted = ? WHERE id = ?", deletedAt, m.ID)
			if err != nil {
				log.Printf("Error marking row %d as deleted: %v", m.ID, err)
			}
		}
		return nil
	}

	if o.DeleteRows {
		for _, m := range filtered {
			fmt.Fprintf(stdout, "Deleting: %s\n", m.Path)
			_, _ = db.Exec("DELETE FROM history WHERE media_id = ?", m.ID)
			_, err = db.Exec("DELETE FROM media WHERE id = ?", m.ID)
			if err != nil {
				log.Printf("Error deleting row %d: %v", m.ID, err)
			}
		}
		return nil
	}

	tabsOpened := 0
	for _, m := range filtered {
		for _, urlToOpen := range linkTargets(m.Path, o.Prefix) {
			fmt.Fprintln(stdout, urlToOpen)
			if o.NoBrowser {
			} else {
				tabsOpened++
				if delay := browserThrottleDelay(tabsOpened); delay > 0 {
					sleep(delay)
				}
				if err := openBrowser(urlToOpen, o.Browser); err != nil {
					log.Printf("Error opening browser: %v", err)
				}
			}
		}
		if !o.NoMarkWatched {
			_, _ = db.Exec("INSERT INTO history (media_id, time_played) VALUES (?, ?)", m.ID, time.Now().Unix())
		}
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
	if len(patterns) == 0 {
		regexs = append(regexs, regexp.MustCompile(`\b\w\w+\b`))
	} else {
		for _, p := range patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				log.Printf("Invalid regex %s: %v", p, err)
				continue
			}
			regexs = append(regexs, re)
		}
	}

	corpus := make([][]string, len(media))
	for i, m := range media {
		processedPath := strings.TrimPrefix(m.Path, "http://")
		processedPath = strings.TrimPrefix(processedPath, "https://")
		corpus[i] = lineSplitter(regexs, processedPath)
	}

	corpusStats := make(map[string]int)
	for _, words := range corpus {
		for _, word := range words {
			corpusStats[strings.ToLower(word)]++
		}
	}

	type mediaInfo struct {
		m         Media
		words     []string
		allUnique bool
		allDup    bool
	}

	infos := make([]mediaInfo, len(media))
	for i, m := range media {
		allUnique := len(corpus[i]) > 0
		allDup := len(corpus[i]) > 0
		for _, w := range corpus[i] {
			count := corpusStats[strings.ToLower(w)]
			if count > 1 {
				allUnique = false
			} else {
				allDup = false
			}
		}
		infos[i] = mediaInfo{m, corpus[i], allUnique, allDup}
	}

	sort.SliceStable(infos, func(i, j int) bool {
		// 1. -allunique (lines that are NOT all unique come first)
		if infos[i].allUnique != infos[j].allUnique {
			return !infos[i].allUnique && infos[j].allUnique
		}

		// 2. alldup (lines that ARE all duplicates come first)
		if infos[i].allDup != infos[j].allDup {
			return infos[i].allDup && !infos[j].allDup
		}

		// 3. alpha (alphabetical sort of words)
		w1 := strings.ToLower(strings.Join(infos[i].words, " "))
		w2 := strings.ToLower(strings.Join(infos[j].words, " "))
		if w1 != w2 {
			return w1 < w2
		}

		// 4. line (original path)
		return strings.ToLower(infos[i].m.Path) < strings.ToLower(infos[j].m.Path)
	})

	result := make([]Media, len(media))
	for i, info := range infos {
		result[i] = info.m
	}
	return result
}

func lineSplitter(regexs []*regexp.Regexp, line string) []string {
	words := []string{line}
	for _, rgx := range regexs {
		var newWords []string
		for _, word := range words {
			matches := rgx.FindAllString(word, -1)
			if matches != nil {
				newWords = append(newWords, matches...)
			}
		}
		words = newWords
	}
	return words
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

func normalizedPrefixes(prefixes []string) []string {
	if len(prefixes) == 0 {
		return []string{defaultPrefix}
	}

	normalized := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		prefix = strings.TrimSuffix(prefix, "%s")
		prefix = strings.TrimSuffix(prefix, "%")
		normalized = append(normalized, prefix)
	}
	return normalized
}

func browserThrottleDelay(tabNumber int) time.Duration {
	switch {
	case tabNumber <= 1:
		return 0
	case tabNumber > 20:
		return 800 * time.Millisecond
	case tabNumber >= 7:
		return 500 * time.Millisecond
	default:
		return 50 * time.Millisecond
	}
}

func linkTargets(path string, prefixes []string) []string {
	if strings.HasPrefix(path, "http") {
		return []string{path}
	}

	targets := make([]string, 0, len(prefixes))
	for _, prefix := range normalizedPrefixes(prefixes) {
		targets = append(targets, prefix+url.QueryEscape(path))
	}
	return targets
}

func browserCommand(browser, url string) (string, []string) {
	browser = strings.TrimSpace(browser)
	if browser != "" {
		fields := strings.Fields(browser)
		if len(fields) > 0 {
			return fields[0], append(fields[1:], url)
		}
	}

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

	return cmd, args
}

func openBrowser(url, browser string) error {
	cmd, args := browserCommand(browser, url)
	return startProcess(cmd, args...)
}

func main() {
	ctx := kong.Parse(&CLI)
	err := ctx.Run()
	if err != nil {
		log.Fatal(err)
	}
}
