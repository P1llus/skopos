// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"log"
	"net/http"
	"strconv"
)

type csvResponseScenario struct{}

// CSVResponse returns the csv_response scenario. It mounts two unauthenticated
// GET handlers serving the same events as text/csv:
//
//   - /csv_response/present  — a header row precedes the data rows.
//   - /csv_response/absent   — data rows only, no header.
//   - /csv_response/gzip     — the header form, gzip-compressed (a file
//     payload, not transport compression), for the gzip→csv chain.
//
// They match templates/csv_response.yml (decode: [{csv: {header: present}}]),
// templates/csv_response_headerless.yml (header: absent), and
// templates/gzip_csv_response.yml (decode: [{gzip: {}}, {csv: {header:
// present}}]). Pagination is none.
func CSVResponse() Scenario { return csvResponseScenario{} }

func (csvResponseScenario) Name() string { return "csv_response" }

func (csvResponseScenario) Register(mux *http.ServeMux, opts Options) {
	present := &EventStore{}
	absent := &EventStore{}
	gz := &EventStore{}
	mux.HandleFunc("/csv_response/present", func(w http.ResponseWriter, r *http.Request) {
		serveCSV(w, r, present, opts, true)
	})
	mux.HandleFunc("/csv_response/absent", func(w http.ResponseWriter, r *http.Request) {
		serveCSV(w, r, absent, opts, false)
	})
	mux.HandleFunc("/csv_response/gzip", func(w http.ResponseWriter, r *http.Request) {
		serveGzipCSV(w, r, gz, opts)
	})
}

// serveGzipCSV writes store's events as a gzip-compressed CSV (header form),
// served as a file payload (Content-Type application/gzip, no
// Content-Encoding) so the transport does not transparently decompress it.
func serveGzipCSV(w http.ResponseWriter, r *http.Request, store *EventStore, opts Options) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	store.Replenish(opts.Now(), opts.EventsPerDrain)
	events := store.Slice(0, opts.EventsPerDrain)
	rows := make([][]string, 0, len(events))
	for _, e := range events {
		rows = append(rows, []string{e.ID, e.Timestamp.Format("2006-01-02T15:04:05Z07:00"), strconv.Itoa(e.SeqNum)})
	}
	writeGzipCSV(w, []string{"id", "timestamp", "seq_num"}, rows)
}

// writeGzipCSV writes header + rows as a gzip-compressed CSV file payload:
// Content-Type application/gzip with no Content-Encoding, so the HTTP
// transport does not transparently decompress it and the spec's decode chain
// owns both the decompression and the CSV parse.
func writeGzipCSV(w http.ResponseWriter, header []string, rows [][]string) {
	var csvBuf bytes.Buffer
	cw := csv.NewWriter(&csvBuf)
	_ = cw.Write(header)
	for _, row := range rows {
		_ = cw.Write(row)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		writeError(w, http.StatusInternalServerError, "csv")
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(csvBuf.Bytes()); err != nil {
		writeError(w, http.StatusInternalServerError, "gzip")
		return
	}
	if err := zw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "gzip close")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("testserver: write gzip csv body: %v", err)
	}
}

// serveCSV replenishes store and writes its events as text/csv, with a leading
// id,timestamp,seq_num header row when header is true.
func serveCSV(w http.ResponseWriter, r *http.Request, store *EventStore, opts Options, header bool) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	store.Replenish(opts.Now(), opts.EventsPerDrain)
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if header {
		_ = cw.Write([]string{"id", "timestamp", "seq_num"})
	}
	for _, e := range store.Slice(0, opts.EventsPerDrain) {
		_ = cw.Write([]string{e.ID, e.Timestamp.Format("2006-01-02T15:04:05Z07:00"), strconv.Itoa(e.SeqNum)})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		writeError(w, http.StatusInternalServerError, "csv")
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("testserver: write csv body: %v", err)
	}
}
