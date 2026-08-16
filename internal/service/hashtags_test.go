package service

import (
	"reflect"
	"testing"
)

func TestExtractHashtags(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "no hashtags",
			content: "just plain text with no tags",
			want:    nil,
		},
		{
			name:    "single hashtag",
			content: "#link",
			want:    []string{"link"},
		},
		{
			name:    "hashtag embedded mid text",
			content: "check this out #link https://example.com/foo",
			want:    []string{"link"},
		},
		{
			name:    "multiple hashtags",
			content: "#link #reading",
			want:    []string{"link", "reading"},
		},
		{
			name:    "case-insensitive dedupe, lowercased output",
			content: "#Link some words #LINK and #link again",
			want:    []string{"link"},
		},
		{
			name:    "allows letters numbers underscore and dash",
			content: "#stack-explorer #go_lang #v2",
			want:    []string{"stack-explorer", "go_lang", "v2"},
		},
		{
			name:    "bare url with no hashtag",
			content: "https://example.com",
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractHashtags(tc.content)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractHashtags(%q) = %#v, want %#v", tc.content, got, tc.want)
			}
		})
	}
}

func TestExtractFirstURL(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantURL   string
		wantFound bool
	}{
		{
			name:      "no url",
			content:   "#link only, no url here",
			wantURL:   "",
			wantFound: false,
		},
		{
			name:      "bare url",
			content:   "https://example.com",
			wantURL:   "https://example.com",
			wantFound: true,
		},
		{
			name:      "url embedded mid text",
			content:   "check this out #link https://example.com/foo",
			wantURL:   "https://example.com/foo",
			wantFound: true,
		},
		{
			name:      "trims trailing sentence punctuation",
			content:   "see https://example.com/foo.",
			wantURL:   "https://example.com/foo",
			wantFound: true,
		},
		{
			name:      "returns first url when multiple present",
			content:   "https://first.example.com and https://second.example.com",
			wantURL:   "https://first.example.com",
			wantFound: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, gotFound := ExtractFirstURL(tc.content)
			if gotFound != tc.wantFound || gotURL != tc.wantURL {
				t.Fatalf("ExtractFirstURL(%q) = (%q, %v), want (%q, %v)", tc.content, gotURL, gotFound, tc.wantURL, tc.wantFound)
			}
		})
	}
}
