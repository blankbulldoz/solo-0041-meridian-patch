package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/delta"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

func openDatabase(dir string) (*db.Database, error) {
	return db.Open(dir)
}

// runDiff builds an MPC v1 package from a baseline MPB image to a target MPB
// image entirely on local files, so two offline deployments can exchange a small
// package without a running service.
func runDiff(args []string) error {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	baselinePath := flags.String("baseline", "", "baseline MPB v1 image file")
	targetPath := flags.String("target", "", "target MPB v1 image file")
	outPath := flags.String("out", "", "output MPC v1 change package")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *baselinePath == "" || *targetPath == "" || *outPath == "" {
		return errors.New("diff requires -baseline, -target and -out")
	}
	base, err := decodeImageFile(*baselinePath)
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	target, err := decodeImageFile(*targetPath)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	patch, err := db.DiffImages(base, target)
	if err != nil {
		return err
	}
	raw, err := delta.Encode(patch)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, raw, 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes): baseline_revision=%d target_revision=%d layers=%d\n",
		*outPath, len(raw), patch.BaselineRevision, patch.TargetRevision, len(patch.Layers))
	return nil
}

// runApplyPatch reconstructs a target image from a baseline image and a change
// package, again purely on local files. It refuses anything whose baseline does
// not match and verifies the reconstructed content digest before writing.
func runApplyPatch(args []string) error {
	flags := flag.NewFlagSet("apply-patch", flag.ContinueOnError)
	baselinePath := flags.String("baseline", "", "baseline MPB v1 image file")
	patchPath := flags.String("patch", "", "MPC v1 change package")
	outPath := flags.String("out", "", "output MPB v1 target image")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *baselinePath == "" || *patchPath == "" || *outPath == "" {
		return errors.New("apply-patch requires -baseline, -patch and -out")
	}
	base, err := decodeImageFile(*baselinePath)
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	raw, err := os.ReadFile(*patchPath)
	if err != nil {
		return err
	}
	patch, err := delta.Decode(raw)
	if err != nil {
		return err
	}
	result, changed, err := delta.Apply(base, patch)
	if err != nil {
		return err
	}
	// A reconstructed image starts at revision zero. Changed layers take the
	// target's commit revision; untouched layers keep their baseline layer
	// revision, matching the semantics the database applies on import.
	result.Revision = patch.TargetRevision
	changedSet := make(map[string]bool, len(changed))
	for _, name := range changed {
		changedSet[name] = true
	}
	for index := range result.Layers {
		name := result.Layers[index].Definition.Name
		if changedSet[name] {
			result.Layers[index].Revision = patch.TargetRevision
		} else {
			for _, layer := range base.Layers {
				if layer.Definition.Name == name {
					result.Layers[index].Revision = layer.Revision
					break
				}
			}
		}
	}
	if err := delta.ValidateImage(result); err != nil {
		return err
	}
	encoded, err := folio.Encode(result)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, encoded, 0o600); err != nil {
		return err
	}
	digest, _ := delta.ContentDigest(result)
	fmt.Printf("wrote %s (%d bytes): revision=%d digest=%s changed_layers=%d\n",
		*outPath, len(encoded), result.Revision, digest, len(changed))
	return nil
}

func decodeImageFile(path string) (catalog.CatalogImage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return catalog.CatalogImage{}, err
	}
	image, err := folio.Decode(raw)
	if err != nil {
		return catalog.CatalogImage{}, err
	}
	if err := delta.ValidateImage(image); err != nil {
		return catalog.CatalogImage{}, err
	}
	return image, nil
}
