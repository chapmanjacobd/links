package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestRegexSort(t *testing.T) {
	tests := []struct {
		name     string
		media    []Media
		patterns []string
		expected []string
	}{
		{
			name: "BasicSortWithDups",
			media: []Media{
				{Path: "red apple"},
				{Path: "broccoli"},
				{Path: "yellow"},
				{Path: "green"},
				{Path: "orange apple"},
				{Path: "red apple"},
			},
			patterns: nil, // Default regex \b\w\w+\b
			// Word corpus counts: red:2, apple:3, broccoli:1, yellow:1, green:1, orange:1
			// "red apple": [red, apple] - allUnique: false, alpha: "red apple", allDup: true
			// "broccoli": [broccoli] - allUnique: true, alpha: "broccoli", allDup: false
			// "yellow": [yellow] - allUnique: true, alpha: "yellow", allDup: false
			// "green": [green] - allUnique: true, alpha: "green", allDup: false
			// "orange apple": [orange, apple] - allUnique: false, alpha: "orange apple", allDup: false
			// "red apple": [red, apple] - allUnique: false, alpha: "red apple", allDup: true
			//
			// 1. -allUnique: red apple, orange apple, red apple come first
			// 2. alpha:
			//    - orange apple
			//    - red apple
			//    - red apple
			// 3. alldup:
			//    - red apple (true)
			//    - red apple (true)
			//    - orange apple (false)
			//
			// Final result for first group: red apple, red apple, orange apple
			// Second group (-allUnique is false): broccoli, green, yellow
			expected: []string{
				"red apple",
				"red apple",
				"orange apple",
				"broccoli",
				"green",
				"yellow",
			},
		},
		{
			name: "CustomRegexSort",
			media: []Media{
				{Path: "https://example.com/a/10"},
				{Path: "https://example.com/b/2"},
				{Path: "https://example.com/a/5"},
			},
			patterns: []string{`[ab]`, `\d+`},
			// a/10 -> [a, 10]
			// b/2 -> [b, 2]
			// a/5 -> [a, 5]
			// Counts: a:2, 10:1, b:1, 2:1, 5:1
			// "a/10": allUnique: false, alpha: "a 10", allDup: false
			// "b/2": allUnique: true, alpha: "b 2", allDup: false
			// "a/5": allUnique: false, alpha: "a 5", allDup: false
			//
			// 1. -allUnique: a/10, a/5 come first
			// 2. alpha: "a 10" vs "a 5" -> "a 10" < "a 5"
			expected: []string{
				"https://example.com/a/10",
				"https://example.com/a/5",
				"https://example.com/b/2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := regexSort(tt.media, tt.patterns)
			var paths []string
			for _, m := range result {
				paths = append(paths, m.Path)
			}
			if !reflect.DeepEqual(paths, tt.expected) {
				t.Errorf("regexSort() = %v, want %v", paths, tt.expected)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://google.com/", "https://google.com"},
		{"https://google.com", "https://google.com"},
		{"  https://google.com/  ", "https://google.com"},
		{"https://example.com/path/", "https://example.com/path"},
		{"", ""},
	}

	for _, tt := range tests {
		result := normalizeURL(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestSetPage(t *testing.T) {
	tests := []struct {
		name        string
		inputURL    string
		pageKey     string
		pageNum     int
		pageReplace string
		expected    string
	}{
		{
			name:     "AddsQueryParameter",
			inputURL: "https://example.com/search?q=go",
			pageKey:  "page",
			pageNum:  2,
			expected: "https://example.com/search?page=2&q=go",
		},
		{
			name:        "ReplacesPlaceholder",
			inputURL:    "https://example.com/page/{n}",
			pageKey:     "page",
			pageNum:     3,
			pageReplace: "{n}",
			expected:    "https://example.com/page/3",
		},
		{
			name:     "LeavesInvalidURLUnchanged",
			inputURL: "://bad url",
			pageKey:  "page",
			pageNum:  1,
			expected: "://bad url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := setPage(tt.inputURL, tt.pageKey, tt.pageNum, tt.pageReplace); got != tt.expected {
				t.Fatalf("setPage() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestExtractLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<html><head><base href="/base/"></head><body>
					<a href="relative/">relative</a>
					<a href="relative/">duplicate</a>
					<a href="child/two?x=1#fragment">child</a>
					<a href="//example.org/cdn/">protocol-relative</a>
					<a href="https://other.example.com/path/">absolute</a>
					<a href="mailto:test@example.com">mailto</a>
					<a href="javascript:void(0)">javascript</a>
					<a href="#section">fragment</a>
				</body></html>`)
		case "/bad-status":
			w.Header().Set("Content-Type", "text/html")
			http.Error(w, "nope", http.StatusBadGateway)
		case "/not-html":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldClient := httpClient
	client := server.Client()
	client.Timeout = time.Second
	httpClient = client
	defer func() { httpClient = oldClient }()

	t.Run("ResolvesRelativeURLs", func(t *testing.T) {
		got, err := extractLinks(server.URL + "/page")
		if err != nil {
			t.Fatalf("extractLinks() error = %v", err)
		}

		expected := []string{
			server.URL + "/base/relative",
			server.URL + "/base/child/two?x=1",
			"http://example.org/cdn",
			"https://other.example.com/path",
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("extractLinks() = %v, want %v", got, expected)
		}
	})

	t.Run("RejectsBadStatus", func(t *testing.T) {
		_, err := extractLinks(server.URL + "/bad-status")
		if err == nil || err.Error() != "unexpected status code: 502 Bad Gateway" {
			t.Fatalf("extractLinks() error = %v, want %q", err, "unexpected status code: 502 Bad Gateway")
		}
	})

	t.Run("RejectsNonHTML", func(t *testing.T) {
		_, err := extractLinks(server.URL + "/not-html")
		if err == nil || err.Error() != "unexpected content type: application/json" {
			t.Fatalf("extractLinks() error = %v, want %q", err, "unexpected content type: application/json")
		}
	})
}

func TestFilterMaxSameDomain(t *testing.T) {
	media := []Media{
		{Path: "https://a.com/1", Hostname: "a.com"},
		{Path: "https://a.com/2", Hostname: "a.com"},
		{Path: "https://b.com/1", Hostname: "b.com"},
	}

	t.Run("Max1", func(t *testing.T) {
		result := filterMaxSameDomain(media, 1)
		if len(result) != 2 {
			t.Errorf("Expected 2 links, got %d", len(result))
		}
		if result[0].Path != "https://a.com/1" || result[1].Path != "https://b.com/1" {
			t.Errorf("Unexpected links: %v", result)
		}
	})

	t.Run("Max2", func(t *testing.T) {
		result := filterMaxSameDomain(media, 2)
		if len(result) != 3 {
			t.Errorf("Expected 3 links, got %d", len(result))
		}
	})
}

func TestBrowserCommand(t *testing.T) {
	url := "https://example.com"

	t.Run("Override", func(t *testing.T) {
		cmd, args := browserCommand("firefox --new-window", url)
		if cmd != "firefox" {
			t.Fatalf("browserCommand override cmd = %q, want %q", cmd, "firefox")
		}
		expected := []string{"--new-window", url}
		if !reflect.DeepEqual(args, expected) {
			t.Fatalf("browserCommand override args = %v, want %v", args, expected)
		}
	})

	t.Run("Default", func(t *testing.T) {
		cmd, args := browserCommand("", url)
		var expectedCmd string
		switch runtime.GOOS {
		case "windows":
			expectedCmd = "rundll32"
		case "darwin":
			expectedCmd = "open"
		default:
			expectedCmd = "xdg-open"
		}
		if cmd != expectedCmd {
			t.Fatalf("browserCommand default cmd = %q, want %q", cmd, expectedCmd)
		}
		expectedArgs := []string{url}
		if runtime.GOOS == "windows" {
			expectedArgs = []string{"url.dll,FileProtocolHandler", url}
		}
		if !reflect.DeepEqual(args, expectedArgs) {
			t.Fatalf("browserCommand default args = %v, want %v", args, expectedArgs)
		}
	})
}

func TestLinkTargets(t *testing.T) {
	t.Run("UsesDefaultPrefix", func(t *testing.T) {
		got := linkTargets("golang kong", nil)
		want := []string{"https://duckduckgo.com/?q=golang+kong"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("linkTargets() = %v, want %v", got, want)
		}
	})

	t.Run("ExpandsMultiplePrefixesAndStripsPlaceholders", func(t *testing.T) {
		got := linkTargets("golang kong", []string{
			"https://duckduckgo.com/?q=%s",
			"https://google.com/search?q=%",
		})
		want := []string{
			"https://duckduckgo.com/?q=golang+kong",
			"https://google.com/search?q=golang+kong",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("linkTargets() = %v, want %v", got, want)
		}
	})

	t.Run("LeavesURLsUnchanged", func(t *testing.T) {
		got := linkTargets("https://example.com", []string{
			"https://duckduckgo.com/?q=",
			"https://google.com/search?q=",
		})
		want := []string{"https://example.com"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("linkTargets() = %v, want %v", got, want)
		}
	})
}

func TestBrowserThrottleDelay(t *testing.T) {
	tests := []struct {
		name      string
		tabNumber int
		want      time.Duration
	}{
		{name: "FirstTab", tabNumber: 1, want: 0},
		{name: "EarlyTabs", tabNumber: 2, want: 50 * time.Millisecond},
		{name: "SixthTab", tabNumber: 6, want: 50 * time.Millisecond},
		{name: "SeventhTab", tabNumber: 7, want: 500 * time.Millisecond},
		{name: "TwentiethTab", tabNumber: 20, want: 500 * time.Millisecond},
		{name: "TwentyFirstTab", tabNumber: 21, want: 800 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := browserThrottleDelay(tt.tabNumber); got != tt.want {
				t.Fatalf("browserThrottleDelay(%d) = %v, want %v", tt.tabNumber, got, tt.want)
			}
		})
	}
}

func TestOpenCmdRunNoBrowserMarksHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	if err := addLink(db, "golang kong", ""); err != nil {
		t.Fatalf("addLink() error = %v", err)
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	oldStartProcess := startProcess
	startProcess = func(cmd string, args ...string) error {
		t.Fatalf("startProcess(%q, %v) should not have been called", cmd, args)
		return nil
	}
	defer func() { startProcess = oldStartProcess }()

	oldSleep := sleep
	sleep = func(d time.Duration) {
		t.Fatalf("sleep(%v) should not have been called", d)
	}
	defer func() { sleep = oldSleep }()

	cmd := OpenCmd{
		DBPath:    dbPath,
		Limit:     1,
		NoBrowser: true,
		Search:    []string{"golang"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	expected := "https://duckduckgo.com/?q=golang+kong\n"
	if output.String() != expected {
		t.Fatalf("printed output = %q, want %q", output.String(), expected)
	}

	db = mustInitDB(t, dbPath)
	defer db.Close()
	if got := historyCount(t, db); got != 1 {
		t.Fatalf("history count = %d, want 1", got)
	}
}

func TestOpenCmdRunMultiplePrefixes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	if err := addLink(db, "golang kong", ""); err != nil {
		t.Fatalf("addLink() error = %v", err)
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	var gotCalls [][]string
	oldStartProcess := startProcess
	startProcess = func(cmd string, args ...string) error {
		call := append([]string{cmd}, args...)
		gotCalls = append(gotCalls, call)
		return nil
	}
	defer func() { startProcess = oldStartProcess }()

	var gotSleeps []time.Duration
	oldSleep := sleep
	sleep = func(d time.Duration) {
		gotSleeps = append(gotSleeps, d)
	}
	defer func() { sleep = oldSleep }()

	cmd := OpenCmd{
		DBPath: dbPath,
		Limit:  1,
		Prefix: []string{
			"https://duckduckgo.com/?q=%s",
			"https://google.com/search?q=%",
		},
		Search: []string{"golang"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	expectedOutput := "https://duckduckgo.com/?q=golang+kong\nhttps://google.com/search?q=golang+kong\n"
	if output.String() != expectedOutput {
		t.Fatalf("printed output = %q, want %q", output.String(), expectedOutput)
	}

	var expectedCmd string
	switch runtime.GOOS {
	case "windows":
		expectedCmd = "rundll32"
	case "darwin":
		expectedCmd = "open"
	default:
		expectedCmd = "xdg-open"
	}

	expectedCalls := [][]string{
		{expectedCmd, "https://duckduckgo.com/?q=golang+kong"},
		{expectedCmd, "https://google.com/search?q=golang+kong"},
	}
	if runtime.GOOS == "windows" {
		expectedCalls = [][]string{
			{expectedCmd, "url.dll,FileProtocolHandler", "https://duckduckgo.com/?q=golang+kong"},
			{expectedCmd, "url.dll,FileProtocolHandler", "https://google.com/search?q=golang+kong"},
		}
	}
	if !reflect.DeepEqual(gotCalls, expectedCalls) {
		t.Fatalf("startProcess calls = %v, want %v", gotCalls, expectedCalls)
	}
	if !reflect.DeepEqual(gotSleeps, []time.Duration{50 * time.Millisecond}) {
		t.Fatalf("sleep calls = %v, want %v", gotSleeps, []time.Duration{50 * time.Millisecond})
	}

	db = mustInitDB(t, dbPath)
	defer db.Close()
	if got := historyCount(t, db); got != 1 {
		t.Fatalf("history count = %d, want 1", got)
	}
}

func TestOpenCmdRunBrowserOverride(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	if err := addLink(db, "https://example.com", ""); err != nil {
		t.Fatalf("addLink() error = %v", err)
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	var gotCmd string
	var gotArgs []string
	oldStartProcess := startProcess
	startProcess = func(cmd string, args ...string) error {
		gotCmd = cmd
		gotArgs = append([]string(nil), args...)
		return nil
	}
	defer func() { startProcess = oldStartProcess }()

	cmd := OpenCmd{
		DBPath:  dbPath,
		Limit:   1,
		Browser: "firefox --new-window",
		Search:  []string{"example"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	if output.String() != "https://example.com\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "https://example.com\n")
	}
	if gotCmd != "firefox" {
		t.Fatalf("browser override cmd = %q, want %q", gotCmd, "firefox")
	}
	expectedArgs := []string{"--new-window", "https://example.com"}
	if !reflect.DeepEqual(gotArgs, expectedArgs) {
		t.Fatalf("browser override args = %v, want %v", gotArgs, expectedArgs)
	}

	db = mustInitDB(t, dbPath)
	defer db.Close()
	if got := historyCount(t, db); got != 1 {
		t.Fatalf("history count = %d, want 1", got)
	}
}

func TestOpenCmdRunNoMarkWatched(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	if err := addLink(db, "https://example.com", ""); err != nil {
		t.Fatalf("addLink() error = %v", err)
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	oldStartProcess := startProcess
	startProcess = func(cmd string, args ...string) error {
		t.Fatalf("startProcess(%q, %v) should not have been called", cmd, args)
		return nil
	}
	defer func() { startProcess = oldStartProcess }()

	cmd := OpenCmd{
		DBPath:        dbPath,
		Limit:         1,
		NoBrowser:     true,
		NoMarkWatched: true,
		Search:        []string{"example"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	if output.String() != "https://example.com\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "https://example.com\n")
	}

	db = mustInitDB(t, dbPath)
	defer db.Close()
	if got := historyCount(t, db); got != 0 {
		t.Fatalf("history count = %d, want 0", got)
	}
}

func TestOpenCmdRunAll(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	for _, p := range []string{"golang kong", "golang docs", "rust book"} {
		if err := addLink(db, p, ""); err != nil {
			t.Fatalf("addLink() error = %v", err)
		}
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	oldStartProcess := startProcess
	startProcess = func(cmd string, args ...string) error {
		t.Fatalf("startProcess(%q, %v) should not have been called", cmd, args)
		return nil
	}
	defer func() { startProcess = oldStartProcess }()

	cmd := OpenCmd{
		DBPath: dbPath,
		All:    true,
		Search: []string{"golang"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	if output.String() != "2\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "2\n")
	}

	db = mustInitDB(t, dbPath)
	defer db.Close()
	if got := historyCount(t, db); got != 0 {
		t.Fatalf("history count = %d, want 0", got)
	}
}

func TestOpenCmdRunAllNoMatches(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	if err := addLink(db, "golang kong", ""); err != nil {
		t.Fatalf("addLink() error = %v", err)
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	cmd := OpenCmd{
		DBPath: dbPath,
		All:    true,
		Search: []string{"nothing-matches"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	if output.String() != "0\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "0\n")
	}
}

func TestOpenCmdRunAllCountsBeforeLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	for _, p := range []string{"golang kong", "golang docs", "golang faq"} {
		if err := addLink(db, p, ""); err != nil {
			t.Fatalf("addLink() error = %v", err)
		}
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	cmd := OpenCmd{
		DBPath: dbPath,
		All:    true,
		Limit:  1,
		Search: []string{"golang"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	if output.String() != "3\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "3\n")
	}
}

func TestOpenCmdRunAllConflicts(t *testing.T) {
	cmd := OpenCmd{
		All:         true,
		DeleteRows:  true,
		MarkDeleted: true,
	}

	err := cmd.Run()
	if err == nil || err.Error() != "--delete-rows and --mark-deleted cannot be used together" {
		t.Fatalf("OpenCmd.Run() error = %v, want %q", err, "--delete-rows and --mark-deleted cannot be used together")
	}

	cmd = OpenCmd{All: true, DeleteRows: true}
	err = cmd.Run()
	if err == nil || err.Error() != "--all cannot be used with --delete-rows or --mark-deleted" {
		t.Fatalf("OpenCmd.Run() error = %v, want %q", err, "--all cannot be used with --delete-rows or --mark-deleted")
	}
}

func TestOpenCmdRunDeleteModesConflict(t *testing.T) {
	cmd := OpenCmd{
		DeleteRows:  true,
		MarkDeleted: true,
	}

	err := cmd.Run()
	if err == nil || err.Error() != "--delete-rows and --mark-deleted cannot be used together" {
		t.Fatalf("OpenCmd.Run() error = %v, want %q", err, "--delete-rows and --mark-deleted cannot be used together")
	}
}

func TestOpenCmdRunMarkDeleted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	if err := addLink(db, "https://example.com", ""); err != nil {
		t.Fatalf("addLink() error = %v", err)
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	oldStartProcess := startProcess
	startProcess = func(cmd string, args ...string) error {
		t.Fatalf("startProcess(%q, %v) should not have been called", cmd, args)
		return nil
	}
	defer func() { startProcess = oldStartProcess }()

	cmd := OpenCmd{
		DBPath:      dbPath,
		Limit:       1,
		MarkDeleted: true,
		NoBrowser:   true,
		Search:      []string{"example"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	if output.String() != "Marking deleted: https://example.com\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "Marking deleted: https://example.com\n")
	}

	db = mustInitDB(t, dbPath)
	defer db.Close()
	if got := mediaCount(t, db, "https://example.com"); got != 1 {
		t.Fatalf("media count = %d, want 1", got)
	}
	if deletedAt := mediaDeletedAt(t, db, "https://example.com"); deletedAt == 0 {
		t.Fatalf("time_deleted = %d, want non-zero", deletedAt)
	}
	if got := historyCount(t, db); got != 0 {
		t.Fatalf("history count = %d, want 0", got)
	}

	output.Reset()
	cmd = OpenCmd{
		DBPath:    dbPath,
		Limit:     1,
		NoBrowser: true,
		Search:    []string{"example"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}
	if output.String() != "No links found\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "No links found\n")
	}
}

func TestOpenCmdRunDeleteRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "links.db")
	db := mustInitDB(t, dbPath)
	if err := addLink(db, "https://example.com", ""); err != nil {
		t.Fatalf("addLink() error = %v", err)
	}
	db.Close()

	var output bytes.Buffer
	oldStdout := stdout
	stdout = &output
	defer func() { stdout = oldStdout }()

	oldStartProcess := startProcess
	startProcess = func(cmd string, args ...string) error {
		t.Fatalf("startProcess(%q, %v) should not have been called", cmd, args)
		return nil
	}
	defer func() { startProcess = oldStartProcess }()

	cmd := OpenCmd{
		DBPath:     dbPath,
		Limit:      1,
		DeleteRows: true,
		NoBrowser:  true,
		Search:     []string{"example"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("OpenCmd.Run() error = %v", err)
	}

	if output.String() != "Deleting: https://example.com\n" {
		t.Fatalf("printed output = %q, want %q", output.String(), "Deleting: https://example.com\n")
	}

	db = mustInitDB(t, dbPath)
	defer db.Close()
	if got := mediaCount(t, db, "https://example.com"); got != 0 {
		t.Fatalf("media count = %d, want 0", got)
	}
	if got := historyCount(t, db); got != 0 {
		t.Fatalf("history count = %d, want 0", got)
	}
}

func mustInitDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()

	db, err := initDB(dbPath)
	if err != nil {
		t.Fatalf("initDB() error = %v", err)
	}
	return db
}

func historyCount(t *testing.T, db *sql.DB) int {
	t.Helper()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM history").Scan(&count); err != nil {
		t.Fatalf("history count query error = %v", err)
	}
	return count
}

func mediaCount(t *testing.T, db *sql.DB, path string) int {
	t.Helper()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM media WHERE path = ?", path).Scan(&count); err != nil {
		t.Fatalf("media count query error = %v", err)
	}
	return count
}

func mediaDeletedAt(t *testing.T, db *sql.DB, path string) int64 {
	t.Helper()

	var deletedAt int64
	if err := db.QueryRow("SELECT time_deleted FROM media WHERE path = ?", path).Scan(&deletedAt); err != nil {
		t.Fatalf("time_deleted query error = %v", err)
	}
	return deletedAt
}
