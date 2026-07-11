//go:build !windows

package player

import "context"

type PotPlayerAdapter struct{}

func NewPotPlayerAdapter() *PotPlayerAdapter { return &PotPlayerAdapter{} }
func (*PotPlayerAdapter) Start(context.Context, string, string) (LaunchResult, error) {
	return LaunchResult{}, ErrUnsupported
}
