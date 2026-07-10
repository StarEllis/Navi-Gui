//go:build !windows

package service

import "os"

func replaceNFOFileAtomic(source, target string) error {
	return os.Rename(source, target)
}
