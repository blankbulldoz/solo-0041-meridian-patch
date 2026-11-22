//go:build unix && !linux

package folio

import "fmt"

// lockDeviceKey is only exact where the OS exposes a matching kernel table
// (Linux /proc/locks). Elsewhere it is a stable identity for correlation with
// `lsof` and `fstat` output.
func lockDeviceKey(dev uint64) string { return fmt.Sprintf("%d", dev) }

func filesystemSupport(dir string) (kind string, warnings []string) {
	return "unknown", []string{"filesystem lock support is not classified on this platform; -probe-lock record is authoritative"}
}
