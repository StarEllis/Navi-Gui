package main

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const applicationLogMaxSize = 20 << 20

func newApplicationLogger(path string) (*zap.Logger, *os.File, error) {
	if info, err := os.Stat(path); err == nil && info.Size() >= applicationLogMaxSize {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}

	fileEncoderConfig := zap.NewProductionEncoderConfig()
	fileEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	fileEncoderConfig.EncodeDuration = zapcore.StringDurationEncoder

	consoleEncoderConfig := zap.NewDevelopmentEncoderConfig()
	consoleEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	level := zap.NewAtomicLevelAt(zap.DebugLevel)
	core := zapcore.NewTee(
		zapcore.NewCore(zapcore.NewConsoleEncoder(consoleEncoderConfig), zapcore.Lock(os.Stderr), level),
		zapcore.NewCore(zapcore.NewJSONEncoder(fileEncoderConfig), zapcore.AddSync(file), level),
	)
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel)), file, nil
}
