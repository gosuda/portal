package main

import (
	"bytes"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/html"

	_ "embed"
)

//go:embed polyfill.js
var polyfillJS []byte

func InjectHTML(body []byte) []byte {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse HTML")
		return body
	}

	// Find the head or body element
	var head *html.Node
	var bodyNode *html.Node
	var crawler func(*html.Node)
	crawler = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "head":
				head = node
			case "body":
				bodyNode = node
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			crawler(child)
		}
	}
	crawler(doc)

	// Create script element
	script := &html.Node{
		Type: html.ElementNode,
		Data: "script",
		Attr: []html.Attribute{},
	}

	// Add the script content
	scriptContent := &html.Node{
		Type: html.TextNode,
		Data: string(polyfillJS),
	}
	script.AppendChild(scriptContent)

	// Inject into head if available, otherwise into body
	if head != nil {
		// Insert as the first child of head
		if head.FirstChild != nil {
			head.InsertBefore(script, head.FirstChild)
		} else {
			head.AppendChild(script)
		}
	} else if bodyNode != nil {
		// Insert as the first child of body if head doesn't exist
		if bodyNode.FirstChild != nil {
			bodyNode.InsertBefore(script, bodyNode.FirstChild)
		} else {
			bodyNode.AppendChild(script)
		}
	}

	// Convert back to bytes
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		log.Error().Err(err).Msg("Failed to render HTML")
		return body
	}

	return buf.Bytes()
}
