#import <Cocoa/Cocoa.h>
#import <string.h>
#import <WebKit/WebKit.h>

// A thin native window around the local cockpit. The kernel still serves the
// same single-page app; this shell only starts it, shows it, and stops the HTTP
// client on quit. Workers run in detached sessions, so quitting does not abort
// an in-flight objective.

static NSString *const kAppName = @"ProjectOS";
static NSString *const kWorkspaceDefaultsKey = @"ProjectOSWorkspacePath";
static const int kDefaultPort = 8799;
static BOOL gStartedServer = NO;

static NSString *Env(NSString *key) {
    NSString *value = [[[NSProcessInfo processInfo] environment] objectForKey:key];
    value = [value stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]];
    return value.length ? value : nil;
}

static NSString *StateDirectory(void) {
    // An explicit environment variable is useful for automation and --check.
    NSString *override = Env(@"PROJECTOS_DIR");
    if (override) return [override stringByExpandingTildeInPath];
    NSString *saved = [[NSUserDefaults standardUserDefaults] stringForKey:kWorkspaceDefaultsKey];
    saved = [saved stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]];
    if (saved.length) return [saved stringByExpandingTildeInPath];
    NSArray *paths = NSSearchPathForDirectoriesInDomains(NSApplicationSupportDirectory, NSUserDomainMask, YES);
    NSString *base = paths.firstObject ?: [NSHomeDirectory() stringByAppendingPathComponent:@"Library/Application Support"];
    return [[base stringByAppendingPathComponent:kAppName] stringByAppendingPathComponent:@"state"];
}

static NSString *KernelBinary(void) {
    NSString *override = Env(@"PROJECTOS_KERNEL");
    if (override && [[NSFileManager defaultManager] isExecutableFileAtPath:override]) return override;
    NSString *bundled = [[NSBundle mainBundle] pathForResource:@"kernel" ofType:nil];
    if (bundled && [[NSFileManager defaultManager] isExecutableFileAtPath:bundled]) return bundled;
    NSString *local = [[[NSFileManager defaultManager] currentDirectoryPath] stringByAppendingPathComponent:@"bin/kernel"];
    if ([[NSFileManager defaultManager] isExecutableFileAtPath:local]) return local;
    return nil;
}

static int RunKernel(NSArray<NSString *> *arguments, NSString **output) {
    NSString *binary = KernelBinary();
    if (!binary) {
        if (output) *output = @"kernel binary not found";
        return -1;
    }
    NSTask *task = [[NSTask alloc] init];
    task.executableURL = [NSURL fileURLWithPath:binary];
    task.arguments = arguments;
    NSPipe *pipe = [NSPipe pipe];
    task.standardOutput = pipe;
    task.standardError = pipe;
    @try {
        [task launch];
        [task waitUntilExit];
    } @catch (NSException *exception) {
        if (output) *output = exception.reason ?: @"failed to launch kernel";
        return -1;
    }
    NSData *data = [pipe.fileHandleForReading readDataToEndOfFile];
    if (output) *output = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding] ?: @"";
    return task.terminationStatus;
}

static NSString *ServerAddress(NSString *dir) {
    NSString *path = [dir stringByAppendingPathComponent:@"serve.json"];
    NSData *data = [NSData dataWithContentsOfFile:path];
    if (!data) return nil;
    NSDictionary *raw = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
    NSString *addr = [raw isKindOfClass:[NSDictionary class]] ? raw[@"addr"] : nil;
    return [addr isKindOfClass:[NSString class]] && addr.length ? addr : nil;
}

static NSString *ControlToken(void) {
    NSString *path = [StateDirectory() stringByAppendingPathComponent:@"control-token"];
    NSString *token = [NSString stringWithContentsOfFile:path encoding:NSUTF8StringEncoding error:nil];
    token = [token stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]];
    return token.length ? token : nil;
}

static NSString *EnsureServer(void) {
    NSString *dir = StateDirectory();
    [[NSFileManager defaultManager] createDirectoryAtPath:dir withIntermediateDirectories:YES attributes:nil error:nil];

    NSString *output = nil;
    int status = RunKernel(@[@"serve", @"--dir", dir, @"--status"], &output);
    if (status == 0 && [output containsString:@"alive=true"]) {
        NSString *addr = ServerAddress(dir);
        if (addr) return [@"http://" stringByAppendingString:addr];
    }

    NSString *addr = [NSString stringWithFormat:@"127.0.0.1:%d", kDefaultPort];
    gStartedServer = YES;
    status = RunKernel(@[@"serve", @"--dir", dir, @"--control", @"--daemon", @"--addr", addr], &output);
    if (status != 0) return nil;
    NSString *actual = ServerAddress(dir);
    return [@"http://" stringByAppendingString:actual ?: addr];
}

static void StopServerFor(NSString *dir) {
    NSString *output = nil;
    RunKernel(@[@"serve", @"--dir", dir, @"--stop"], &output);
}

static void StopServer(void) {
    StopServerFor(StateDirectory());
}

@interface AppDelegate : NSObject <NSApplicationDelegate>
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) WKWebView *webView;
@end

@implementation AppDelegate

- (void)installMenu {
    NSMenu *mainMenu = [[NSMenu alloc] init];

    NSMenuItem *appItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:appItem];
    NSMenu *appMenu = [[NSMenu alloc] init];
    [appItem setSubmenu:appMenu];
    NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:@"退出 ProjectOS"
                                                  action:@selector(terminate:)
                                           keyEquivalent:@"q"];
    quit.target = NSApp;
    [appMenu addItem:quit];

    NSMenuItem *fileItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:fileItem];
    NSMenu *fileMenu = [[NSMenu alloc] initWithTitle:@"文件"];
    [fileItem setSubmenu:fileMenu];
    NSMenuItem *choose = [[NSMenuItem alloc] initWithTitle:@"选择工作区…"
                                                    action:@selector(chooseWorkspace:)
                                             keyEquivalent:@"o"];
    choose.target = self;
    [fileMenu addItem:choose];

    NSMenuItem *editItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:editItem];
    NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"编辑"];
    [editItem setSubmenu:editMenu];
    NSArray *edits = @[
        @[@"撤销", @"undo:", @"z"],
        @[@"重做", @"redo:", @"Z"],
        @[@"剪切", @"cut:", @"x"],
        @[@"复制", @"copy:", @"c"],
        @[@"粘贴", @"paste:", @"v"],
        @[@"全选", @"selectAll:", @"a"],
    ];
    for (NSArray *entry in edits) {
        NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:entry[0]
                                                      action:NSSelectorFromString(entry[1])
                                               keyEquivalent:entry[2]];
        [editMenu addItem:item];
    }

    [NSApp setMainMenu:mainMenu];
}

- (void)loadCockpit:(NSString *)base {
    NSString *token = ControlToken();
    NSString *url = token ? [NSString stringWithFormat:@"%@/chat#token=%@", base, token]
                          : [base stringByAppendingString:@"/chat"];
    [self.webView loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:url]]];
    self.window.title = [NSString stringWithFormat:@"ProjectOS — %@", StateDirectory()];
    self.window.representedURL = [NSURL fileURLWithPath:StateDirectory()];
}

- (void)chooseWorkspace:(id)sender {
    NSOpenPanel *panel = [NSOpenPanel openPanel];
    panel.canChooseDirectories = YES;
    panel.canChooseFiles = NO;
    panel.allowsMultipleSelection = NO;
    panel.canCreateDirectories = YES;
    panel.prompt = @"使用此工作区";
    panel.message = @"选择 ProjectOS 的工作区目录。切换后会重启本地服务，已托管任务不会被取消。";
    panel.directoryURL = [NSURL fileURLWithPath:StateDirectory()];
    if ([panel runModal] != NSModalResponseOK) return;

    NSString *old = StateDirectory();
    NSString *next = panel.URL.path;
    if ([next isEqualToString:old]) return;

    if (gStartedServer) {
        StopServerFor(old);
        gStartedServer = NO;
    }
    [[NSUserDefaults standardUserDefaults] setObject:next forKey:kWorkspaceDefaultsKey];
    [[NSUserDefaults standardUserDefaults] synchronize];

    NSString *base = EnsureServer();
    if (!base) {
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"无法切换工作区";
        alert.informativeText = [NSString stringWithFormat:@"无法在 %@ 启动 ProjectOS。已恢复上一个工作区。", next];
        [alert runModal];
        [[NSUserDefaults standardUserDefaults] setObject:old forKey:kWorkspaceDefaultsKey];
        [[NSUserDefaults standardUserDefaults] synchronize];
        NSString *restored = EnsureServer();
        if (restored) [self loadCockpit:restored];
        return;
    }
    [self loadCockpit:base];
}

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    [self installMenu];
    NSString *base = EnsureServer();
    if (!base) {
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"ProjectOS 无法启动";
        alert.informativeText = @"检查 kernel 二进制是否在 Resources 中，或设置 PROJECTOS_KERNEL。";
        [alert runModal];
        [NSApp terminate:nil];
        return;
    }

    WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
    self.webView = [[WKWebView alloc] initWithFrame:NSZeroRect configuration:config];

    NSRect frame = NSMakeRect(0, 0, 1280, 860);
    self.window = [[NSWindow alloc] initWithContentRect:frame
                                              styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                                                         NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable)
                                                backing:NSBackingStoreBuffered
                                                  defer:NO];
    self.window.title = [NSString stringWithFormat:@"ProjectOS — %@", StateDirectory()];
    self.window.representedURL = [NSURL fileURLWithPath:StateDirectory()];
    [self.window center];
    self.window.contentView = self.webView;
    [self.window makeKeyAndOrderFront:nil];
    // Put the control token in the fragment so the cockpit is usable without
    // asking the user to open control-token in a terminal. Fragments never reach
    // the server and the page removes it from the address bar after storing it.
    [self loadCockpit:base];
    [NSApp activateIgnoringOtherApps:YES];
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender { return YES; }

- (void)applicationWillTerminate:(NSNotification *)notification {
    // Only stop a server this window started; an already-running daemon may be
    // serving other clients or objectives.
    if (gStartedServer) StopServer();
}

@end

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        BOOL check = NO;
        for (int i = 1; i < argc; i++) {
            if (strcmp(argv[i], "--check") == 0) check = YES;
        }
        if (check) {
            NSString *base = EnsureServer();
            if (!base) {
                fprintf(stderr, "server did not start\n");
                return 1;
            }
            BOOL ok = NO;
            for (int i = 0; i < 40 && !ok; i++) {
                NSURL *url = [NSURL URLWithString:[base stringByAppendingString:@"/api/status"]];
                NSData *data = [NSData dataWithContentsOfURL:url];
                ok = data.length > 0;
                if (!ok) [NSThread sleepForTimeInterval:0.25];
            }
            StopServer();
            printf("%s %s\n", ok ? "ok" : "status request failed", base.UTF8String);
            return ok ? 0 : 1;
        }

        NSApplication *app = [NSApplication sharedApplication];
        AppDelegate *delegate = [[AppDelegate alloc] init];
        app.delegate = delegate;
        [app setActivationPolicy:NSApplicationActivationPolicyRegular];
        [app run];
    }
    return 0;
}
