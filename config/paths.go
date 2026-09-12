package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// dataFolderName 是 exe 旁边那个统筹文件夹。除了 exe 自己，
// 程序运行时产生的东西全部关在这里面，别人拿到的目录只有一个 exe 加一个文件夹。
const dataFolderName = "data"

var (
	appDirOnce sync.Once
	appDirPath string
)

// AppDir 返回 exe 所在目录。
//
// 用 os.Executable 而不是当前工作目录——从快捷方式或开机自启启动时，
// 系统给的工作目录不一定是 exe 目录，按它走会在别的地方再建一份空库。
func AppDir() string {
	appDirOnce.Do(func() {
		if custom := strings.TrimSpace(os.Getenv("NAVI_DATA_DIR")); custom != "" {
			if absolutePath, err := filepath.Abs(custom); err == nil {
				appDirPath = absolutePath
				return
			}
		}

		executablePath, err := os.Executable()
		if err != nil {
			// 拿不到 exe 路径时退回相对当前目录，至少不至于起不来。
			appDirPath = "."
			return
		}
		if resolvedPath, err := filepath.EvalSymlinks(executablePath); err == nil {
			executablePath = resolvedPath
		}
		appDirPath = filepath.Dir(executablePath)
	})

	return appDirPath
}

// DataDir 返回统筹文件夹：<exe 目录>/data。
func DataDir() string {
	return filepath.Join(AppDir(), dataFolderName)
}

// DataPath 拼出统筹文件夹下的路径。
func DataPath(elements ...string) string {
	return filepath.Join(append([]string{DataDir()}, elements...)...)
}
