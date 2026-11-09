package agent

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipped compresses a JSON answer for a client that accepts it.
//
// Lists are text and repeat themselves, and the apps poll them: on 2026-11-09
// a phone's Agents app fetched a 290 KB /v1/tasks from a laptop every few
// seconds, ~150 kB/s for hours, and the phone's battery went with it. gzip
// takes such a list to a fraction of that, and Android's HttpURLConnection
// asks for it and unpacks it on its own, so the apps already installed gain
// without a change. Only for whole answers: an event stream is flushed line
// by line and is not wrapped.
func gzipped(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		h(gzipWriter{ResponseWriter: w, w: zw}, r)
	}
}

type gzipWriter struct {
	http.ResponseWriter
	w *gzip.Writer
}

func (g gzipWriter) Write(b []byte) (int, error) { return g.w.Write(b) }

func (g gzipWriter) WriteHeader(code int) {
	g.ResponseWriter.Header().Del("Content-Length")
	g.ResponseWriter.WriteHeader(code)
}
