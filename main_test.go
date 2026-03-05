package main

import (
	"reflect"
	"testing"
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
