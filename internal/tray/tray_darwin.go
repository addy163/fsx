//go:build darwin && cgo

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include <pthread.h>
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface FSXClient : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKDownloadDelegate>
@property(nonatomic, strong) NSStatusItem *status;
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) WKWebView *web;
@property(nonatomic, copy) NSString *url;
@property(nonatomic, copy) NSString *pairCode;
@property(nonatomic, copy) NSString *nodeID;
@end

@implementation FSXClient

- (void)boot {
	NSApplication *app = [NSApplication sharedApplication];
	[app setActivationPolicy:NSApplicationActivationPolicyAccessory];
	[app setDelegate:self];

	self.status = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
	NSImage *img = [NSImage imageWithSystemSymbolName:@"arrow.up.arrow.down" accessibilityDescription:@"fsx"];
	if (img) [img setTemplate:YES];
	self.status.button.image = img;
	self.status.button.toolTip = @"fsx · 文件传送";

	NSMenu *m = [[NSMenu alloc] init];
	NSMenuItem *it;
	it = [[NSMenuItem alloc] initWithTitle:@"打开主窗口" action:@selector(showWindow) keyEquivalent:@""];
	it.target = self; [m addItem:it];
	[m addItem:[NSMenuItem separatorItem]];
	it = [[NSMenuItem alloc] initWithTitle:[NSString stringWithFormat:@"复制配对码 · %@", self.pairCode]
		action:@selector(copyCode) keyEquivalent:@""];
	it.target = self; [m addItem:it];
	it = [[NSMenuItem alloc] initWithTitle:[NSString stringWithFormat:@"复制节点 ID · %@",
		[self.nodeID substringToIndex:MIN((NSUInteger)8, self.nodeID.length)]]
		action:@selector(copyID) keyEquivalent:@""];
	it.target = self; [m addItem:it];
	[m addItem:[NSMenuItem separatorItem]];
	it = [[NSMenuItem alloc] initWithTitle:@"退出 fsx" action:@selector(quit) keyEquivalent:@"q"];
	it.target = self; [m addItem:it];
	self.status.menu = m;

	WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
	self.web = [[WKWebView alloc] initWithFrame:NSMakeRect(0, 0, 1280, 860) configuration:cfg];
	self.web.navigationDelegate = self;
	self.window = [[NSWindow alloc]
		initWithContentRect:NSMakeRect(0, 0, 1280, 860)
		styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable)
		backing:NSBackingStoreBuffered defer:NO];
	self.window.title = @"fsx · 文件传送";
	self.window.minSize = NSMakeSize(940, 620);
	self.window.contentView = self.web;
	self.window.delegate = self;
	[self.window center];
	[self.web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:self.url]]];
	[self showWindow];
}

- (void)showWindow {
	[self.window makeKeyAndOrderFront:nil];
	[NSApp activateIgnoringOtherApps:YES];
}

- (void)copyCode { [self copyToPasteboard:self.pairCode]; }
- (void)copyID { [self copyToPasteboard:self.nodeID]; }
- (void)copyToPasteboard:(NSString *)s {
	NSPasteboard *p = [NSPasteboard generalPasteboard];
	[p clearContents];
	[p setString:s forType:NSPasteboardTypeString];
}
- (void)quit { [self requestQuit]; }

// requestQuit 让 [NSApp run] 正常返回（terminate: 会直接 exit，跳过 Go 侧清理）。
- (void)requestQuit {
	[NSApp stop:nil];
	NSEvent *ev = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
		location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0
		context:nil subtype:0 data1:0 data2:0];
	[NSApp postEvent:ev atStart:YES];
}

- (BOOL)windowShouldClose:(NSWindow *)w { [w orderOut:nil]; return NO; }
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)app { return NO; }

- (void)webView:(WKWebView *)wv didFinishNavigation:(WKNavigation *)nav {
	NSLog(@"[fsx] 控制台已加载 %@", wv.URL.absoluteString);
}
- (void)webView:(WKWebView *)wv didFailProvisionalNavigation:(WKNavigation *)nav withError:(NSError *)e {
	NSLog(@"[fsx] 控制台加载失败: %@", e.localizedDescription);
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.0 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
		[self.web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:self.url]]];
	});
}
- (void)webView:(WKWebView *)wv decidePolicyForNavigationAction:(WKNavigationAction *)a
	decisionHandler:(void (^)(WKNavigationActionPolicy))handler {
	handler(WKNavigationActionPolicyAllow);
}
- (void)webView:(WKWebView *)wv navigationAction:(WKNavigationAction *)a
	shouldPerformDownload:(void (^)(BOOL))handler {
	handler([a.request.URL.path hasPrefix:@"/dl/"]);
}
- (void)download:(WKDownload *)dl decideDestinationUsingResponse:(NSURLResponse *)resp
	suggestedFilename:(NSString *)filename completionHandler:(void (^)(NSURL *))handler {
	NSString *dir = [NSSearchPathForDirectoriesInDomains(NSDownloadsDirectory, NSUserDomainMask, YES) firstObject];
	NSString *name = filename.length ? filename : @"fsx-download";
	NSString *base = [name stringByDeletingPathExtension];
	NSString *ext = [name pathExtension];
	NSFileManager *fm = [NSFileManager defaultManager];
	NSURL *u = [NSURL fileURLWithPath:[dir stringByAppendingPathComponent:name]];
	for (int i = 1; [fm fileExistsAtPath:u.path]; i++) {
		NSString *n = ext.length ? [NSString stringWithFormat:@"%@-%d.%@", base, i, ext]
			: [NSString stringWithFormat:@"%@-%d", base, i];
		u = [NSURL fileURLWithPath:[dir stringByAppendingPathComponent:n]];
	}
	NSLog(@"[fsx] 另存为 %@", u.path);
	handler(u);
}
@end

static FSXClient *gClient = nil;

static void fsx_boot(const char *url, const char *code, const char *nid) {
	gClient = [[FSXClient alloc] init];
	gClient.url = [NSString stringWithUTF8String:url];
	gClient.pairCode = [NSString stringWithUTF8String:code];
	gClient.nodeID = [NSString stringWithUTF8String:nid];
	[gClient boot];
}
static void fsx_run(void) { [NSApp run]; }
static void fsx_show(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [gClient showWindow]; });
}
static void fsx_quit(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [gClient requestQuit]; });
}
static void fsx_alert(const char *title, const char *msg) {
	NSString *t = [NSString stringWithUTF8String:title];
	NSString *m = [NSString stringWithUTF8String:msg];
	void (^show)(void) = ^{
		NSAlert *a = [[NSAlert alloc] init];
		a.messageText = t;
		a.informativeText = m;
		[a runModal];
	};
	if (pthread_main_np()) show();
	else dispatch_sync(dispatch_get_main_queue(), show);
}
*/
import "C"

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Supported 仅在以 .app bundle 启动时为真：终端里直跑二进制保持命令行行为。
func Supported() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

func HideConsole() {}

func ShowConsole() {}

func SetupLog(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "fsx.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	syscall.Dup2(int(f.Fd()), 1)
	syscall.Dup2(int(f.Fd()), 2)
	return func() { f.Close() }, nil
}

func Error(title, text string) {
	ct, cm := C.CString(title), C.CString(text)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cm))
	C.fsx_alert(ct, cm)
}

func sockPath(home string) string { return filepath.Join(home, "fsx.app.sock") }

// NotifyExisting 通知已在运行的实例弹出主窗口；没有实例在跑时返回 false。
func NotifyExisting(home string) bool {
	c, err := net.DialTimeout("unix", sockPath(home), 400*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(400 * time.Millisecond))
	_, err = c.Write([]byte("show"))
	return err == nil
}

func Run(o Options) error {
	runtime.LockOSThread()
	p := sockPath(o.Home)
	if c, err := net.DialTimeout("unix", p, 200*time.Millisecond); err != nil {
		os.Remove(p)
	} else {
		c.Close()
	}
	if ln, err := net.Listen("unix", p); err == nil {
		defer ln.Close()
		defer os.Remove(p)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
				C.fsx_show()
			}
		}()
	}
	cu, cc, cn := C.CString(o.URL), C.CString(o.PairCode), C.CString(o.NodeID)
	defer C.free(unsafe.Pointer(cu))
	defer C.free(unsafe.Pointer(cc))
	defer C.free(unsafe.Pointer(cn))
	C.fsx_boot(cu, cc, cn)
	C.fsx_run()
	return nil
}

func Quit() { C.fsx_quit() }