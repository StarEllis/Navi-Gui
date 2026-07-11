//go:build windows && potplayer_integration

package player

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Run manually with a disposable media file. It opens a real PotPlayer instance.
func TestPotPlayerIntegrationLaunchAndBind(t *testing.T) {
	executable := os.Getenv("NAVI_POTPLAYER_EXE")
	mediaFile := os.Getenv("NAVI_POTPLAYER_TEST_MEDIA")
	if executable == "" || mediaFile == "" {
		t.Skip("set NAVI_POTPLAYER_EXE and NAVI_POTPLAYER_TEST_MEDIA")
	}
	result, err := NewPotPlayerAdapter().Start(context.Background(), executable, mediaFile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Session == nil {
		t.Fatalf("playback started without bind: %v", result.Warning)
	}
	defer result.Session.Detach()
	if _, err := result.Session.Sample(context.Background()); err != nil && !errors.Is(err, ErrNotLoaded) {
		t.Fatal(err)
	}
}
