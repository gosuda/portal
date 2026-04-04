package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/portal/v2/types"
)

const ogMaxBufferSize = 1 << 20

func newOGInjectHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
		if idx := strings.LastIndex(host, ":"); idx != -1 {
			if !strings.Contains(host[idx:], "]") {
				host = host[:idx]
			}
		}

		if r.URL.Path == "/og-banner.png" {
			pngData, err := renderOGBannerPNG(host)
			if err != nil {
				http.Error(w, "failed to generate og image", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", strconv.Itoa(len(pngData)))
			w.Header().Set("Cache-Control", "public, max-age=300")
			_, _ = w.Write(pngData)
			return
		}

		ogw := &ogResponseWriter{
			ResponseWriter: w,
			host:           host,
		}
		next.ServeHTTP(ogw, r)
		ogw.finish()
	})
}

type ogResponseWriter struct {
	http.ResponseWriter
	host       string
	buf        bytes.Buffer
	buffering  bool
	statusCode int
	headerDone bool
	finished   bool
}

func (w *ogResponseWriter) WriteHeader(code int) {
	if w.headerDone {
		return
	}
	w.headerDone = true
	w.statusCode = code

	ct := w.Header().Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		cl := w.Header().Get("Content-Length")
		if cl == "" {
			w.buffering = true
		} else if size, err := strconv.ParseInt(cl, 10, 64); err == nil && size < ogMaxBufferSize {
			w.buffering = true
		}
	}

	if !w.buffering {
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *ogResponseWriter) Write(b []byte) (int, error) {
	if !w.headerDone {
		w.WriteHeader(http.StatusOK)
	}
	if w.buffering {
		if w.buf.Len()+len(b) > ogMaxBufferSize {
			w.buffering = false
			w.ResponseWriter.WriteHeader(w.statusCode)
			if w.buf.Len() > 0 {
				_, _ = w.ResponseWriter.Write(w.buf.Bytes())
				w.buf.Reset()
			}
			return w.ResponseWriter.Write(b)
		}
		return w.buf.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *ogResponseWriter) finish() {
	if w.finished {
		return
	}
	w.finished = true

	if !w.buffering || w.buf.Len() == 0 {
		return
	}

	body := w.buf.String()
	lower := strings.ToLower(body)

	if !strings.Contains(lower, "og:image") {
		headIdx := strings.Index(lower, "</head>")
		if headIdx >= 0 {
			imageURL := "https://" + w.host + "/og-banner.png?v=" + types.ReleaseVersion
			publicURL := "https://" + w.host

			ogTags := fmt.Sprintf(
				"    <meta property=\"og:image\" content=\"%s\" />\n"+
					"    <meta property=\"og:url\" content=\"%s\" />\n"+
					"    <meta property=\"og:type\" content=\"website\" />\n"+
					"    <meta property=\"og:site_name\" content=\"Portal\" />\n"+
					"    <meta name=\"twitter:card\" content=\"summary_large_image\" />\n"+
					"    <meta name=\"twitter:image\" content=\"%s\" />\n",
				imageURL, publicURL, imageURL,
			)

			body = body[:headIdx] + ogTags + body[headIdx:]
		}
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Del("Content-Encoding")
	w.ResponseWriter.WriteHeader(w.statusCode)
	_, _ = io.WriteString(w.ResponseWriter, body)
}
