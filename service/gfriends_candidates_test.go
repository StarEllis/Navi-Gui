package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeCandidateIndex(t *testing.T, cacheDir string, content map[string]map[string]string) {
	t.Helper()
	// 服务把索引放在 <cacheDir>/gfriends/ 下；写对位置测试才不会去联网拉真索引。
	indexDir := filepath.Join(cacheDir, "gfriends")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"Content": content})
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, "Filetree.json"), payload, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

func TestListCandidatesCollectsEveryStudioVariant(t *testing.T) {
	cacheDir := t.TempDir()
	writeCandidateIndex(t, cacheDir, map[string]map[string]string{
		"0-Hand-Storage": {"桜空もも.jpg": "桜空もも.jpg?t=1"},
		"6-Tpowers": {
			"桜空もも.jpg":   "桜空もも.jpg?t=2",
			"桜空もも-1.jpg": "桜空もも-1.jpg?t=3",
			"桜空もも-2.jpg": "桜空もも-2.jpg?t=4",
		},
		// 同一演员的 AI 修复版，键是原名、值是改过的文件名
		"8-Warashi": {"桜空もも.jpg": "AI-Fix-桜空もも.jpg?t=5"},
		// 名字相近但不是同一个人，不能混进来
		"9-Other": {"森山桜空.jpg": "森山桜空.jpg?t=6"},
	})

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{CacheDir: cacheDir})
	candidates, err := service.ListCandidates("桜空もも")
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 5 {
		t.Fatalf("candidates = %d, want 5: %+v", len(candidates), candidates)
	}
	// 片商目录带排序前缀，顺序必须稳定，否则每次「换一张」跳到的图都不一样。
	want := []string{
		"0-Hand-Storage/桜空もも.jpg",
		"6-Tpowers/桜空もも-1.jpg",
		"6-Tpowers/桜空もも-2.jpg",
		"6-Tpowers/桜空もも.jpg",
		"8-Warashi/AI-Fix-桜空もも.jpg",
	}
	for index, candidate := range candidates {
		if candidate.CandidateKey() != want[index] {
			t.Fatalf("candidate[%d] = %q, want %q", index, candidate.CandidateKey(), want[index])
		}
	}
}

func TestListCandidatesResolvesChineseAliasAndCaps(t *testing.T) {
	cacheDir := t.TempDir()
	files := map[string]string{}
	for index := 1; index <= gfriendsMaxCandidates+5; index++ {
		name := "桜空もも-" + itoa(index) + ".jpg"
		files[name] = name + "?t=1"
	}
	writeCandidateIndex(t, cacheDir, map[string]map[string]string{"9-Eightman": files})

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{CacheDir: cacheDir})
	// 演员名在库里是中文写法，图源按日文归档，得走别名表找过去。
	candidates, err := service.ListCandidates("樱空桃")
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != gfriendsMaxCandidates {
		t.Fatalf("candidates = %d, want the cap %d", len(candidates), gfriendsMaxCandidates)
	}
}

func TestListCandidatesReturnsNothingForUnknownActor(t *testing.T) {
	cacheDir := t.TempDir()
	writeCandidateIndex(t, cacheDir, map[string]map[string]string{
		"0-Hand-Storage": {"桜空もも.jpg": "桜空もも.jpg?t=1"},
	})

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{CacheDir: cacheDir})
	candidates, err := service.ListCandidates("Gabbie Carter")
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
