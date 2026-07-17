package model

import (
	"strings"
	"sync"

	"github.com/longbridgeapp/opencc"
	"golang.org/x/text/unicode/norm"
)

type lazyChineseConverter struct {
	conversion string
	once       sync.Once
	converter  *opencc.OpenCC
}

func (c *lazyChineseConverter) convert(value string) string {
	c.once.Do(func() {
		converter, err := opencc.New(c.conversion)
		if err == nil {
			c.converter = converter
		}
	})
	if c.converter == nil {
		return value
	}
	converted, err := c.converter.Convert(value)
	if err != nil {
		return value
	}
	return converted
}

var (
	traditionalToSimplified = lazyChineseConverter{conversion: "t2s"}
	simplifiedToTraditional = lazyChineseConverter{conversion: "s2t"}
	simplifiedToTaiwan      = lazyChineseConverter{conversion: "s2tw"}
	simplifiedToHongKong    = lazyChineseConverter{conversion: "s2hk"}
	japaneseToSimplified    = strings.NewReplacer(
		"瀬", "濑",
		"沢", "泽",
	)
	simplifiedToJapanese = strings.NewReplacer(
		"濑", "瀬",
		"泽", "沢",
	)
)

func NormalizeChineseVariants(value string) string {
	value = norm.NFKC.String(value)
	value = japaneseToSimplified.Replace(value)
	return traditionalToSimplified.convert(value)
}

// EquivalentPersonNames returns the spellings that should resolve to one person.
func EquivalentPersonNames(name string) []string {
	name = norm.NFKC.String(strings.TrimSpace(name))
	if name == "" {
		return nil
	}

	canonical := NormalizeChineseVariants(name)
	candidates := []string{
		canonical,
		name,
		simplifiedToTraditional.convert(canonical),
		simplifiedToTaiwan.convert(canonical),
		simplifiedToHongKong.convert(canonical),
		simplifiedToJapanese.Replace(canonical),
	}
	seen := make(map[string]bool, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		result = append(result, candidate)
	}
	return result
}
