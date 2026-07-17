package main

import (
	"testing"

	"navi-desktop/model"
	"navi-desktop/service"
)

func TestShouldAutoCompleteDetailMetadataPrioritizesAnyQuickRecord(t *testing.T) {
	tests := []struct {
		name  string
		media *model.Media
		want  bool
	}{
		{
			name: "quick record with NFO runtime",
			media: &model.Media{
				FilePath:      "C:/media/movie.mp4",
				Runtime:       170,
				MetadataPhase: service.MetadataPhaseQuick,
			},
			want: true,
		},
		{
			name: "completed record",
			media: &model.Media{
				FilePath:      "C:/media/movie.mp4",
				MetadataPhase: service.MetadataPhaseFull,
			},
			want: false,
		},
		{
			name: "quick record without file",
			media: &model.Media{
				MetadataPhase: service.MetadataPhaseQuick,
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldAutoCompleteDetailMetadata(test.media); got != test.want {
				t.Fatalf("shouldAutoCompleteDetailMetadata() = %v, want %v", got, test.want)
			}
		})
	}
}
