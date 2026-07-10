//go:build windows

package service

import (
	"os"
	"testing"

	"go.uber.org/zap"
	"golang.org/x/sys/windows"
)

func TestSaveEditorDataOccupiedWindowsFileRemainsUnchanged(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	original := []byte(`<movie><title>Original</title></movie>`)
	path := writeTempNFO(t, string(original))
	data := loadEditorForSave(t, service, path)
	data.Title = "Changed"
	data.UpdatedFields = []string{"title"}

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode NFO path: %v", err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("open occupied NFO fixture: %v", err)
	}
	defer windows.CloseHandle(handle)

	if err := service.SaveEditorData(path, data); err == nil {
		t.Fatal("expected occupied NFO replacement failure")
	}
	assertFileBytes(t, path, original)
	assertNoNFOTempFiles(t, path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("occupied NFO disappeared after failed save: %v", err)
	}
}
