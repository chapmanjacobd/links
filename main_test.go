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
			name: "BasicDupSort",
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
			// "red apple": 2 dups (red, apple)
			// "broccoli": 0 dups
			// "yellow": 0 dups
			// "green": 0 dups
			// "orange apple": 1 dup (apple)
			// "red apple": 2 dups (red, apple)
			// Order by dups desc: "red apple", "red apple", "orange apple", "broccoli", "green", "yellow"
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
			// a/10 -> [a, 10] -> dups: 0? No, let's look at counts.
			// a:2, b:1, 10:1, 2:1, 5:1
			// "a/10": 1 dup (a)
			// "b/2": 0 dups
			// "a/5": 1 dup (a)
			// Order: a/10, a/5, b/2 (a/10 vs a/5 sorted by path since dups and joined words are same)
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
