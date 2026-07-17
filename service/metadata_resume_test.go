package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/repository"
)

func TestResumeInterruptedMetadataCompletionEnqueuesQuickRecordsOnly(t *testing.T) {
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	records := []model.Media{
		{ID: "quick-1", Title: "Quick 1", FilePath: "C:/media/quick-1.mp4", MetadataPhase: MetadataPhaseQuick},
		{ID: "quick-2", Title: "Quick 2", FilePath: "C:/media/quick-2.mp4", MetadataPhase: MetadataPhaseQuick},
		{ID: "full-1", Title: "Full", FilePath: "C:/media/full.mp4", MetadataPhase: MetadataPhaseFull},
		{ID: "failed-1", Title: "Failed", FilePath: "C:/media/failed.mp4", MetadataPhase: MetadataPhaseFailed},
	}
	if err := db.Create(&records).Error; err != nil {
		t.Fatalf("seed media: %v", err)
	}

	repos := repository.NewRepositories(db)
	scanner := &ScannerService{
		mediaRepo:         repos.Media,
		metadataNormal:    make(chan metadataCompletionTask, 4),
		metadataHighPri:   make(chan metadataCompletionTask, 4),
		metadataState:     make(map[string]metadataTaskPriority),
		metadataAccepting: true,
	}

	resumed, err := scanner.ResumeInterruptedMetadataCompletion(context.Background())
	if err != nil {
		t.Fatalf("resume interrupted metadata completion: %v", err)
	}
	if resumed != 2 {
		t.Fatalf("resumed = %d, want 2", resumed)
	}
	if got := len(scanner.metadataNormal); got != 2 {
		t.Fatalf("normal queue length = %d, want 2", got)
	}
	if _, queued := scanner.metadataState["full-1"]; queued {
		t.Fatal("full metadata record was queued")
	}
	if _, queued := scanner.metadataState["failed-1"]; queued {
		t.Fatal("failed metadata record was queued")
	}
}
