// Command mpbverify validates MPB v1 backup images offline.
//
// It performs every check that a production meridian process performs when
// reopening a data directory (signature, length, checksum, canonical JSON,
// structure, revision and geometry rules, and index rebuild), but never opens a
// data directory and never takes the writer lock. Output is one JSON object per
// file, followed by a summary line, and the exit code is non-zero if any image
// is invalid.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

type result struct {
	File  string         `json:"file"`
	OK    bool           `json:"ok"`
	Error string         `json:"error,omitempty"`
	Probe *db.ImageProbe `json:"probe,omitempty"`
}

func inspect(path string) result {
	out := result{File: path}
	file, err := os.Open(path)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, folio.MaxImage+1))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if len(raw) > folio.MaxImage {
		out.Error = "image exceeds maximum size"
		return out
	}
	probe, err := db.InspectImage(raw)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.OK = true
	out.Probe = &probe
	return out
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s image.mpb [image.mpb ...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	failed := 0
	for _, path := range flag.Args() {
		r := inspect(path)
		if !r.OK {
			failed++
		}
		if err := encoder.Encode(r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}
