// Command mpbarchive turns MPB v1 database images into plain-data archives
// for an archiver, verifies them, and classifies the difference between two
// archives without needing the database process.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/archive"
)

const usage = `usage: mpbarchive <command> [arguments]

commands:
  unpack  <image.mpb> <dir>   decode and validate an MPB image into a plain-data archive
  verify  <image.mpb|dir>     check an MPB image or an unpacked archive
  compare <a> <b> [--json]    classify the difference between two archives or images

verdicts: identical, revision-only (只换过提交号), relayout (重新布局), content-changed (内容改变)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "unpack":
		err = cmdUnpack(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mpbarchive: %v\n", err)
		os.Exit(1)
	}
}

func cmdUnpack(args []string) error {
	flags := flag.NewFlagSet("unpack", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("unpack expects <image.mpb> and <dir>")
	}
	image, dir := flags.Arg(0), flags.Arg(1)
	raw, err := os.ReadFile(image)
	if err != nil {
		return err
	}
	m, err := archive.Unpack(raw, filepath.Base(image), dir)
	if err != nil {
		return err
	}
	fmt.Printf("ok unpacked revision=%d layers=%d features=%d into %s\n",
		m.Revision, len(m.Layers), m.Features(), dir)
	return nil
}

func cmdVerify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("verify expects one <image.mpb> or <dir>")
	}
	m, err := loadModel(flags.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("ok %s revision=%d layers=%d features=%d\n",
		flags.Arg(0), m.Revision, len(m.Layers), m.Features())
	return nil
}

func cmdCompare(args []string) error {
	asJSON := false
	var paths []string
	for _, arg := range args {
		if arg == "--json" {
			asJSON = true
		} else {
			paths = append(paths, arg)
		}
	}
	if len(paths) != 2 {
		return fmt.Errorf("compare expects two <image.mpb> or <dir> arguments")
	}
	a, err := loadModel(paths[0])
	if err != nil {
		return err
	}
	b, err := loadModel(paths[1])
	if err != nil {
		return err
	}
	report := archive.Compare(a, b)
	if asJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Print(report.Summary())
	return nil
}

// loadModel reads either an unpacked archive directory or a raw MPB image.
func loadModel(path string) (*archive.Model, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return archive.ReadDir(path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := archive.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	m.Source = path
	return m, nil
}
