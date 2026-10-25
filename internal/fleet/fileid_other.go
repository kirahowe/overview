//go:build !unix

package fleet

import "os"

type fileID struct{}

func fileIDOf(os.FileInfo) fileID { return fileID{} }
