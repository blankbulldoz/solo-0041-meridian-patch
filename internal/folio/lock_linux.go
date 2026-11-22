//go:build unix && linux

package folio

import (
	"fmt"
	"syscall"
)

// lockDeviceKey renders a st_dev value the way /proc/locks and lslocks do, so
// an operator can match the reported key against the kernel's holder line.
func lockDeviceKey(dev uint64) string {
	major := ((dev >> 8) & 0xfff) | ((dev >> 32) &^ 0xfff)
	minor := (dev & 0xff) | ((dev >> 12) &^ 0xff)
	return fmt.Sprintf("%02x:%02x", major, minor)
}

var filesystemNames = map[uint64]string{
	0xEF53:     "ext2/ext3/ext4",
	0x58465342: "xfs",
	0x9123683E: "btrfs",
	0x2FC12FC1: "zfs",
	0x794C7630: "overlayfs",
	0x01021994: "tmpfs",
	0x6969:     "nfs",
	0xFF534D42: "cifs/smb",
	0x65735546: "fuse",
	0xF2F52010: "f2fs",
	0x4D44:     "vfat",
}

// filesystemSupport describes whether the data directory's filesystem is known
// to provide kernel record locks. NFS and SMB delegate locking to a remote lock
// manager, which is the class of deployment that most needs the guard retained.
func filesystemSupport(dir string) (kind string, warnings []string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return "unknown", []string{"could not stat the data directory filesystem: " + err.Error()}
	}
	magic := uint64(uint32(stat.Type))
	kind = filesystemNames[magic]
	if kind == "" {
		kind = fmt.Sprintf("unknown(0x%x)", magic)
	}
	switch magic {
	case 0x6969, 0xFF534D42:
		warnings = append(warnings, fmt.Sprintf(
			"%s: record locks depend on a reachable remote lock manager; keep the flock guard until -probe-lock record and -check-lock pass on the real mount", kind))
	case 0x01021994:
		warnings = append(warnings, "tmpfs: locks work but neither state nor lock survives a reboot")
	case 0x794C7630:
		warnings = append(warnings, "overlayfs: locks are reliable only when the upper and lower layers share one inode view; rehearse on the real mount")
	}
	return kind, warnings
}
