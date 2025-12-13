package main

import (
	"bytes"
	_ "embed"
)

//go:embed polyfill.min.js
var polyfillJS []byte

func InjectHTML(body []byte) []byte {
	// Simple bytes based search to avoid full HTML parsing
	// Look for </head> case insensitive
	lowerBody := bytes.ToLower(body)
	headEnd := []byte("</head>")

	idx := bytes.Index(lowerBody, headEnd)

	scriptTag := []byte("<script>")
	scriptTag = append(scriptTag, polyfillJS...)
	scriptTag = append(scriptTag, []byte("</script>")...)

	if idx != -1 {
		// Insert before </head>
		var buf bytes.Buffer
		buf.Grow(len(body) + len(scriptTag))
		buf.Write(body[:idx])
		buf.Write(scriptTag)
		buf.Write(body[idx:])
		return buf.Bytes()
	}

	// Fallback: look for </body>
	bodyEnd := []byte("</body>")
	idx = bytes.Index(lowerBody, bodyEnd)
	if idx != -1 {
		// Insert before </body>
		var buf bytes.Buffer
		buf.Grow(len(body) + len(scriptTag))
		buf.Write(body[:idx])
		buf.Write(scriptTag)
		buf.Write(body[idx:])
		return buf.Bytes()
	}

	// Fallback: append to end
	return append(body, scriptTag...)
}
