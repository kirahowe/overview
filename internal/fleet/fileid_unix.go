//go:build unix

package fleet

import (
	"os"
	"syscall"
)

// fileID names a file on disk, so a replacement at the same path shows.
type fileID struct{ dev, ino uint64 }

func fileIDOf(info os.FileInfo) fileID {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fileID{uint64(st.Dev), uint64(st.Ino)}
	}
	return fileID{}
}
