// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"log"
	"net/http"
	"strconv"
)

type zipCSVScenario struct{}

// ZipCSV returns the zip_csv scenario. It mounts a single unauthenticated GET
// /zip_csv/archive handler whose body is a ZIP archive of two CSV members
// (part-a.csv, part-b.csv), each with a header row. Members are written in
// reverse name order to prove the decoder sorts by name. Matches
// templates/zip_csv_response.yml's decode: [{zip: {glob: "*.csv"}}, {csv:
// {header: present}}]. Pagination is none.
func ZipCSV() Scenario { return zipCSVScenario{} }

func (zipCSVScenario) Name() string { return "zip_csv" }

func (zipCSVScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/zip_csv/archive", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		events := store.Slice(0, opts.EventsPerDrain)
		half := len(events) / 2

		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		// Written b-then-a on purpose: the decoder must name-sort members so
		// part-a.csv's rows precede part-b.csv's regardless of archive order.
		if err := writeZipCSVMember(zw, "part-b.csv", events[half:]); err != nil {
			writeError(w, http.StatusInternalServerError, "zip member")
			return
		}
		if err := writeZipCSVMember(zw, "part-a.csv", events[:half]); err != nil {
			writeError(w, http.StatusInternalServerError, "zip member")
			return
		}
		// A non-CSV member the glob must skip.
		if mw, err := zw.Create("README.txt"); err == nil {
			_, _ = mw.Write([]byte("not a csv\n"))
		}
		if err := zw.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, "zip close")
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(buf.Bytes()); err != nil {
			log.Printf("testserver: write zip body: %v", err)
		}
	})
}

// writeZipCSVMember adds one CSV member (with a header row) to zw.
func writeZipCSVMember(zw *zip.Writer, name string, events []Event) error {
	mw, err := zw.Create(name)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(mw)
	if err := cw.Write([]string{"id", "timestamp", "seq_num"}); err != nil {
		return err
	}
	for _, e := range events {
		if err := cw.Write([]string{e.ID, e.Timestamp.Format("2006-01-02T15:04:05Z07:00"), strconv.Itoa(e.SeqNum)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
