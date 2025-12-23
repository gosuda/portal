package main

import (
	"strings"
	"testing"
)

func TestInjectOGMetadata(t *testing.T) {
	f := &Frontend{}

	// Set global flag for testing
	flagPortalURL = "https://portal.example.com"

	tests := []struct {
		name        string
		title       string
		description string
		imageURL    string
		html        string
		want        []string // strings that should be present in the output
	}{
		{
			name:        "Basic injection",
			title:       "Hello World",
			description: "This is a test description",
			imageURL:    "https://example.com/image.png",
			html:        "<title>[%OG_TITLE%]</title><meta name=\"description\" content=\"[%OG_DESCRIPTION%]\"><meta property=\"og:image\" content=\"[%OG_IMAGE_URL%]\">",
			want: []string{
				"<title>Hello World</title>",
				"content=\"This is a test description\"",
				"content=\"https://example.com/image.png\"",
			},
		},
		{
			name:        "HTML Escaping",
			title:       "<script>alert('xss')</script>",
			description: "Double \"quotes\" and <tags>",
			imageURL:    "https://example.com/img?q=1&b=2",
			html:        "[%OG_TITLE%] | [%OG_DESCRIPTION%] | [%OG_IMAGE_URL%]",
			want: []string{
				"&lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;",
				"Double &#34;quotes&#34; and &lt;tags&gt;",
				"https://example.com/img?q=1&amp;b=2",
			},
		},
		{
			name:        "Empty values (Defaults)",
			title:       "",
			description: "",
			imageURL:    "",
			html:        "[%OG_TITLE%] | [%OG_DESCRIPTION%] | [%OG_IMAGE_URL%]",
			want: []string{
				"Portal Proxy Gateway",
				"Transform your local services into web-accessible endpoints",
				"https://portal.example.com/portal.jpg",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := f.injectOGMetadata(tt.html, tt.title, tt.description, tt.imageURL)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("injectOGMetadata() = %v, want to contain %v", got, w)
				}
			}
		})
	}
}
