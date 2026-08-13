package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// 封面墙上一张图大约 220px 宽就够看清角标了，原图 100KB+ 一次铺 2500 张会卡死浏览器。
const thumbWidth = 240

var thumbOnce sync.Map // 同一张图并发请求时只生成一次

func thumbCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "NaviTagger", "thumbs")
	os.MkdirAll(dir, 0o755)
	return dir
}

func thumbnail(item *Item) ([]byte, error) {
	info, err := os.Stat(item.Poster)
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(thumbCacheDir(), fmt.Sprintf("%s-%d.jpg", item.ID, info.ModTime().UnixNano()))

	lock, _ := thumbOnce.LoadOrStore(cachePath, &sync.Mutex{})
	mutex := lock.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	if cached, err := os.ReadFile(cachePath); err == nil {
		return cached, nil
	}

	data, err := renderThumbnail(item.Poster)
	if err != nil {
		return nil, err
	}
	os.WriteFile(cachePath, data, 0o644)
	return data, nil
}

func renderThumbnail(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	source, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, downscale(source, thumbWidth), &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// downscale 用方块平均缩图。标准库没带缩放器，但缩小场景下方块平均的
// 效果足够看清封面角标，也省掉一个第三方依赖。
func downscale(source image.Image, maxWidth int) image.Image {
	bounds := source.Bounds()
	if bounds.Dx() <= maxWidth {
		return source
	}

	ratio := float64(bounds.Dx()) / float64(maxWidth)
	width := maxWidth
	height := int(float64(bounds.Dy()) / ratio)
	if height < 1 {
		height = 1
	}
	target := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		top := bounds.Min.Y + int(float64(y)*ratio)
		bottom := bounds.Min.Y + int(float64(y+1)*ratio)
		if bottom > bounds.Max.Y {
			bottom = bounds.Max.Y
		}
		if bottom <= top {
			bottom = top + 1
		}
		for x := 0; x < width; x++ {
			left := bounds.Min.X + int(float64(x)*ratio)
			right := bounds.Min.X + int(float64(x+1)*ratio)
			if right > bounds.Max.X {
				right = bounds.Max.X
			}
			if right <= left {
				right = left + 1
			}

			var sumR, sumG, sumB, count uint32
			for sy := top; sy < bottom; sy++ {
				for sx := left; sx < right; sx++ {
					r, g, b, _ := source.At(sx, sy).RGBA()
					sumR += r >> 8
					sumG += g >> 8
					sumB += b >> 8
					count++
				}
			}
			if count == 0 {
				count = 1
			}
			target.Set(x, y, color.RGBA{
				R: uint8(sumR / count),
				G: uint8(sumG / count),
				B: uint8(sumB / count),
				A: 255,
			})
		}
	}
	return target
}

// warmThumbnails 扫描完成后在后台先把缩略图做好，滚动封面墙时就不会一格一格地卡。
func warmThumbnails(items []*Item) {
	queue := make(chan *Item)
	workers := runtime.NumCPU()
	if workers > 6 {
		workers = 6
	}

	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for item := range queue {
				thumbnail(item)
			}
		}()
	}
	for _, item := range items {
		if item.Poster != "" {
			queue <- item
		}
	}
	close(queue)
	group.Wait()
}
