//go:build windows

package tray

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmContextMenu    = 0x007B
	wmLButtonDblClk  = 0x0203
	wmRButtonUp      = 0x0205
	wmApp            = 0x8000
	wmTray           = wmApp + 1
	wsPopup          = 0x80000000
	swHide           = 0
	swShowNormal     = 1
	csUnicodeDoc     = 13
	gmemMoveable     = 0x0040
	nimAdd           = 0
	nimModify        = 1
	nimDelete        = 2
	nifMessage       = 0x1
	nifIcon          = 0x2
	nifTip           = 0x4
	nifInfo          = 0x10
	niifInfo         = 0x1
	niifNoSound      = 0x10
	mfString         = 0
	mfSeparator      = 0x800
	tpmRightButton   = 0x2
	tpmNoNotify      = 0x80
	tpmReturnCmd     = 0x100
	menuOpenConsole  = 1
	menuCopyCode     = 2
	menuCopyNodeID   = 3
	menuQuit         = 4
	trayIconID       = 1
	iconSize         = 32
	fallbackIconID   = 32512 // IDI_APPLICATION
	logMaxSize int64 = 4 << 20
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")

	pRegisterClassExW      = user32.NewProc("RegisterClassExW")
	pUnregisterClassW      = user32.NewProc("UnregisterClassW")
	pCreateWindowExW       = user32.NewProc("CreateWindowExW")
	pDestroyWindow         = user32.NewProc("DestroyWindow")
	pDefWindowProcW        = user32.NewProc("DefWindowProcW")
	pGetMessageW           = user32.NewProc("GetMessageW")
	pTranslateMessage      = user32.NewProc("TranslateMessage")
	pDispatchMessageW      = user32.NewProc("DispatchMessageW")
	pPostQuitMessage       = user32.NewProc("PostQuitMessage")
	pPostMessageW          = user32.NewProc("PostMessageW")
	pCreatePopupMenu       = user32.NewProc("CreatePopupMenu")
	pAppendMenuW           = user32.NewProc("AppendMenuW")
	pDestroyMenu           = user32.NewProc("DestroyMenu")
	pTrackPopupMenu        = user32.NewProc("TrackPopupMenu")
	pSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	pGetCursorPos          = user32.NewProc("GetCursorPos")
	pCreateIconIndirect    = user32.NewProc("CreateIconIndirect")
	pLoadIconW             = user32.NewProc("LoadIconW")
	pDestroyIcon           = user32.NewProc("DestroyIcon")
	pOpenClipboard         = user32.NewProc("OpenClipboard")
	pEmptyClipboard        = user32.NewProc("EmptyClipboard")
	pCloseClipboard        = user32.NewProc("CloseClipboard")
	pSetClipboardData      = user32.NewProc("SetClipboardData")
	pShowWindow            = user32.NewProc("ShowWindow")
	pGetDC                 = user32.NewProc("GetDC")
	pReleaseDC             = user32.NewProc("ReleaseDC")
	pMessageBoxW           = user32.NewProc("MessageBoxW")
	pShellExecuteW         = shell32.NewProc("ShellExecuteW")
	pShellNotifyIconW      = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW      = kernel32.NewProc("GetModuleHandleW")
	pGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	pGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	pGlobalAlloc           = kernel32.NewProc("GlobalAlloc")
	pGlobalFree            = kernel32.NewProc("GlobalFree")
	pGlobalLock            = kernel32.NewProc("GlobalLock")
	pGlobalUnlock          = kernel32.NewProc("GlobalUnlock")
	pRtlMoveMemory         = kernel32.NewProc("RtlMoveMemory")
	pCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	pCreateBitmap          = gdi32.NewProc("CreateBitmap")
	pDeleteObject          = gdi32.NewProc("DeleteObject")
)

type point struct {
	X int32
	Y int32
}

type msg struct {
	HWnd    syscall.Handle
	Message uint32
	_       uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type notifyIconData struct {
	CbSize           uint32
	_                uint32
	HWnd             syscall.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	_                uint32
	HIcon            syscall.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeout         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     syscall.Handle
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	_        uint32
	HMask    syscall.Handle
	HColor   syscall.Handle
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	Hdr   bitmapInfoHeader
	Color [1]uint32
}

var (
	trayMu    sync.Mutex
	trayHWnd  syscall.Handle
	trayOpts  Options
	trayIcon  syscall.Handle
	iconOwned bool
)

// Supported 报告当前平台是否支持托盘客户端。
func Supported() bool { return true }

// HideConsole 在双击启动（控制台只有本进程）时隐藏命令行窗口。
// 从 cmd/PowerShell 里启动时不隐藏，方便查看输出。
func HideConsole() {
	var pids [4]uint32
	n, _, _ := pGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n > 1 {
		return
	}
	if hwnd, _, _ := pGetConsoleWindow.Call(); hwnd != 0 {
		pShowWindow.Call(hwnd, uintptr(swHide))
	}
}

// ShowConsole 恢复命令行窗口，用于托盘不可用时回退。
func ShowConsole() {
	if hwnd, _, _ := pGetConsoleWindow.Call(); hwnd != 0 {
		pShowWindow.Call(hwnd, uintptr(swShowNormal))
	}
}

// SetupLog 把日志写到状态目录下的 fsx.log，返回关闭函数。
func SetupLog(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "fsx.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > logMaxSize {
		os.Remove(path + ".old")
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags)
	return func() { f.Close() }, nil
}

// Error 在无法打开控制台时给用户一个可见的提示。
func Error(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	x, _ := syscall.UTF16PtrFromString(text)
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(x)), uintptr(unsafe.Pointer(t)), 0x10)
}

// Run 创建隐藏窗口与托盘图标，进入消息循环，直到 Quit 或菜单退出。
func Run(o Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	trayMu.Lock()
	trayOpts = o
	trayMu.Unlock()

	hInst, _, _ := pGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("FsxTrayWindow")

	var wc wndClassEx
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = syscall.NewCallback(wndProc)
	wc.HInstance = syscall.Handle(hInst)
	wc.LpszClassName = className
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("注册托盘窗口失败: %v", err)
	}
	defer pUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), hInst)

	title, _ := syscall.UTF16PtrFromString("fsx")
	hwnd, _, err := pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsPopup, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		return fmt.Errorf("创建托盘窗口失败: %v", err)
	}
	trayMu.Lock()
	trayHWnd = syscall.Handle(hwnd)
	trayMu.Unlock()
	defer func() {
		trayMu.Lock()
		trayHWnd = 0
		trayMu.Unlock()
	}()

	if !addIcon(syscall.Handle(hwnd), o) {
		// 没有资源管理器（或托盘被禁用）时回退到命令行窗口 + 浏览器
		log.Printf("托盘图标添加失败，回退到命令行窗口")
		ShowConsole()
		openConsole()
	}

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	removeIcon()
	return nil
}

// Quit 可从任意 goroutine 调用。
func Quit() {
	trayMu.Lock()
	hwnd := trayHWnd
	trayMu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
	}
}

func wndProc(hwnd syscall.Handle, uMsg uint32, wParam, lParam uintptr) uintptr {
	switch uMsg {
	case wmTray:
		switch uint32(lParam & 0xffff) {
		case wmRButtonUp, wmContextMenu:
			showMenu(hwnd)
		case wmLButtonDblClk:
			openConsole()
		}
		return 0
	case wmClose:
		removeIcon()
		pDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(uMsg), wParam, lParam)
	return r
}

func showMenu(hwnd syscall.Handle) {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)

	appendItem(menu, menuOpenConsole, "打开控制台")
	appendItem(menu, menuCopyCode, "复制配对码")
	appendItem(menu, menuCopyNodeID, "复制节点 ID")
	pAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendItem(menu, menuQuit, "退出")

	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// 先置前台，否则点击菜单外部时菜单不会收起
	pSetForegroundWindow.Call(uintptr(hwnd))
	cmd, _, _ := pTrackPopupMenu.Call(menu,
		tpmRightButton|tpmNoNotify|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
	pPostMessageW.Call(uintptr(hwnd), 0, 0, 0) // WM_NULL

	trayMu.Lock()
	code, nodeID := trayOpts.PairCode, trayOpts.NodeID
	trayMu.Unlock()

	switch int32(cmd) {
	case menuOpenConsole:
		openConsole()
	case menuCopyCode:
		if code != "" {
			copyText(code)
		}
	case menuCopyNodeID:
		if nodeID != "" {
			copyText(nodeID)
		}
	case menuQuit:
		pPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
	}
}

func appendItem(menu uintptr, id int, text string) {
	p, _ := syscall.UTF16PtrFromString(text)
	pAppendMenuW.Call(menu, mfString, uintptr(id), uintptr(unsafe.Pointer(p)))
}

func addIcon(hwnd syscall.Handle, o Options) bool {
	trayMu.Lock()
	trayIcon, iconOwned = makeIcon()
	trayMu.Unlock()

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = trayIconID
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTray
	nid.HIcon = trayIcon
	copyUTF16(nid.SzTip[:], fmt.Sprintf("fsx · %s · 配对码 %s", o.Name, o.PairCode))

	// 开机自启时资源管理器可能还没准备好，重试几次
	ok := false
	for i := 0; i < 10; i++ {
		if r, _, _ := pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r != 0 {
			ok = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ok {
		log.Printf("托盘图标添加失败")
		return false
	}

	nid.UFlags = nifInfo
	nid.DwInfoFlags = niifInfo | niifNoSound
	copyUTF16(nid.SzInfoTitle[:], "fsx 已启动 · "+o.Name)
	copyUTF16(nid.SzInfo[:], fmt.Sprintf("配对码 %s · 双击托盘图标打开控制台", o.PairCode))
	pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	return true
}

func removeIcon() {
	trayMu.Lock()
	hwnd, icon, owned := trayHWnd, trayIcon, iconOwned
	trayIcon, iconOwned = 0, false
	trayMu.Unlock()
	if hwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = trayIconID
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if owned && icon != 0 {
		pDestroyIcon.Call(uintptr(icon))
	}
}

func openConsole() {
	trayMu.Lock()
	url := trayOpts.URL
	trayMu.Unlock()
	if url == "" {
		return
	}
	verb, _ := syscall.UTF16PtrFromString("open")
	file, _ := syscall.UTF16PtrFromString(url)
	pShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, swShowNormal)
}

func copyText(s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	if r, _, _ := pOpenClipboard.Call(0); r == 0 {
		return
	}
	pEmptyClipboard.Call()
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, uintptr(len(u)*2))
	if h != 0 {
		if p, _, _ := pGlobalLock.Call(h); p != 0 {
			pRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)*2))
			pGlobalUnlock.Call(h)
			if r, _, _ := pSetClipboardData.Call(csUnicodeDoc, h); r == 0 {
				pGlobalFree.Call(h)
			}
		} else {
			pGlobalFree.Call(h)
		}
	}
	pCloseClipboard.Call()
}

func copyUTF16(dst []uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(u) > len(dst) {
		u = append(u[:len(dst)-1], 0)
	}
	copy(dst, u)
}

// makeIcon 画一个 32x32 的圆角渐变图标：蓝绿底 + 白色向上箭头。
func makeIcon() (syscall.Handle, bool) {
	var bmi bitmapInfo
	bmi.Hdr.BiSize = uint32(unsafe.Sizeof(bmi.Hdr))
	bmi.Hdr.BiWidth = iconSize
	bmi.Hdr.BiHeight = -iconSize // 自上而下，方便按坐标写像素
	bmi.Hdr.BiPlanes = 1
	bmi.Hdr.BiBitCount = 32

	hdc, _, _ := pGetDC.Call(0)
	var bits unsafe.Pointer
	hbm, _, _ := pCreateDIBSection.Call(hdc, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	pReleaseDC.Call(0, hdc)
	if hbm == 0 || bits == nil {
		return fallbackIcon(), false
	}

	px := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), iconSize*iconSize)
	const (
		topR, topG, topB = 0x4C, 0x8D, 0xFF
		botR, botG, botB = 0x2E, 0xE6, 0xA8
	)
	for y := 0; y < iconSize; y++ {
		t := float64(y) / float64(iconSize-1)
		r := uint32(float64(topR) + (float64(botR)-float64(topR))*t)
		g := uint32(float64(topG) + (float64(botG)-float64(topG))*t)
		b := uint32(float64(topB) + (float64(botB)-float64(topB))*t)
		bg := uint32(0xFF)<<24 | r<<16 | g<<8 | b
		for x := 0; x < iconSize; x++ {
			switch {
			case !inRoundRect(x, y):
				px[y*iconSize+x] = 0
			case inArrow(x, y):
				px[y*iconSize+x] = 0xFFFFFFFF
			default:
				px[y*iconSize+x] = bg
			}
		}
	}

	// 图标使用 alpha 通道，AND 掩码全 0（不额外遮罩）
	zeroMask := make([]byte, iconSize*iconSize/8)
	mask, _, _ := pCreateBitmap.Call(iconSize, iconSize, 1, 1, uintptr(unsafe.Pointer(&zeroMask[0])))

	var ii iconInfo
	ii.FIcon = 1
	ii.HMask = syscall.Handle(mask)
	ii.HColor = syscall.Handle(hbm)
	icon, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	pDeleteObject.Call(hbm)
	if mask != 0 {
		pDeleteObject.Call(mask)
	}
	if icon == 0 {
		return fallbackIcon(), false
	}
	return syscall.Handle(icon), true
}

func fallbackIcon() syscall.Handle {
	h, _, _ := pLoadIconW.Call(0, fallbackIconID)
	return syscall.Handle(h)
}

func inRoundRect(x, y int) bool {
	const r = 7
	cx, cy := x, y
	if cx < r {
		cx = r
	} else if cx > iconSize-1-r {
		cx = iconSize - 1 - r
	}
	if cy < r {
		cy = r
	} else if cy > iconSize-1-r {
		cy = iconSize - 1 - r
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func inArrow(x, y int) bool {
	dx := x - 16
	if dx < 0 {
		dx = -dx
	}
	// 箭头尖
	if y >= 5 && y <= 14 && dx <= y-5 {
		return true
	}
	// 箭杆
	return dx <= 2 && y >= 13 && y <= 26
}
