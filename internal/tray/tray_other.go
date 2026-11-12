//go:build !windows && !(darwin && cgo)

package tray

import "errors"

func Supported() bool { return false }

func HideConsole() {}

func ShowConsole() {}

func SetupLog(dir string) (func(), error) { return func() {}, nil }

func Error(title, text string) {}

func NotifyExisting(home string) bool { return false }

func Run(o Options) error { return errors.New("当前平台不支持托盘模式") }

func Quit() {}
