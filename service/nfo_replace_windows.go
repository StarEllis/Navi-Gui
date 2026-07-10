//go:build windows

package service

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func replaceNFOFileAtomic(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	moveErr := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	if moveErr == nil {
		return nil
	}
	if renameErr := os.Rename(source, target); renameErr != nil {
		return fmt.Errorf("Windows replacement failed (%v), safe rename fallback failed: %w", moveErr, renameErr)
	}
	return nil
}
