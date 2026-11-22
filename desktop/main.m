#import <Cocoa/Cocoa.h>
#import <string.h>
#import <signal.h>
#import <WebKit/WebKit.h>
#import <arpa/inet.h>
#import <netinet/in.h>
#import <sys/socket.h>
#import <unistd.h>

// A thin native window around one local kernel per workspace. Each tab owns a
// workspace directory, its own `kernel serve` instance, and its own WKWebView;
// tabs keep running in parallel while the window shows one at a time. Workers
// run in detached sessions, so quitting does not abort an in-flight objective.

static NSString *const kAppName = @"ProjectOS";
static NSString *const kLegacyWorkspaceKey = @"ProjectOSWorkspacePath";
static NSString *const kWorkspacePathsKey = @"ProjectOSWorkspacePaths";
static NSString *const kActiveWorkspaceKey = @"ProjectOSActiveWorkspacePath";
static const int kDefaultPort = 8799;
static const int kMaxTabCount = 8;

static NSString *Env(NSString *key) {
    NSString *value = [[[NSProcessInfo processInfo] environment] objectForKey:key];
    value = [value stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]];
    return value.length ? value : nil;
}

static NSString *DefaultStateDirectory(void) {
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

// Resolve the OpenCode CLI once so kernels launched from Finder (minimal PATH)
// still use the same v2 binary as the chat runtime.
static NSString *ResolveOpenCodeBin(void) {
    static NSString *resolved = nil;
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        NSString *explicit = Env(@"OPENCODE_BIN");
        if (explicit) { resolved = explicit; return; }
        NSFileManager *fm = [NSFileManager defaultManager];
        NSString *cliRoot = [NSHomeDirectory() stringByAppendingPathComponent:
            @"Library/Application Support/ai.opencode.desktop/cli"];
        NSArray *entries = [fm contentsOfDirectoryAtPath:cliRoot error:nil];
        NSArray *sorted = [entries sortedArrayUsingComparator:^NSComparisonResult(NSString *a, NSString *b) {
            return [b compare:a options:NSNumericSearch];
        }];
        for (NSString *entry in sorted) {
            NSString *path = [[cliRoot stringByAppendingPathComponent:entry]
                stringByAppendingPathComponent:@"opencode-cli"];
            if ([fm isExecutableFileAtPath:path]) { resolved = path; return; }
        }
        NSArray *common = @[@"/opt/homebrew/bin/opencode", @"/usr/local/bin/opencode",
                            [NSHomeDirectory() stringByAppendingPathComponent:@".npm-global/bin/opencode"]];
        for (NSString *path in common) {
            if ([fm isExecutableFileAtPath:path]) { resolved = path; return; }
        }
    });
    return resolved;
}

static int RunKernelTimeout(NSArray<NSString *> *arguments, NSString **output, NSTimeInterval timeout) {
    NSString *binary = KernelBinary();
    if (!binary) {
        if (output) *output = @"kernel binary not found";
        return -1;
    }
    NSTask *task = [[NSTask alloc] init];
    task.executableURL = [NSURL fileURLWithPath:binary];
    task.arguments = arguments;
    if (!Env(@"OPENCODE_BIN")) {
        NSString *openCode = ResolveOpenCodeBin();
        if (openCode) {
            NSMutableDictionary *environment = [[[NSProcessInfo processInfo] environment] mutableCopy];
            environment[@"OPENCODE_BIN"] = openCode;
            task.environment = environment;
        }
    }
    NSPipe *pipe = [NSPipe pipe];
    task.standardOutput = pipe;
    task.standardError = pipe;
    dispatch_semaphore_t exitDone = dispatch_semaphore_create(0);
    task.terminationHandler = ^(NSTask *finished) {
        dispatch_semaphore_signal(exitDone);
    };
    @try {
        [task launch];
    } @catch (NSException *exception) {
        if (output) *output = exception.reason ?: @"failed to launch kernel";
        return -1;
    }
    // Drain the pipe while the child runs. Reading only after exit deadlocks
    // once the child fills the 16-64KB pipe buffer.
    dispatch_semaphore_t readDone = dispatch_semaphore_create(0);
    __block NSData *data = nil;
    dispatch_async(dispatch_get_global_queue(QOS_CLASS_UTILITY, 0), ^{
        data = [pipe.fileHandleForReading readDataToEndOfFile];
        dispatch_semaphore_signal(readDone);
    });
    long timedOut = dispatch_semaphore_wait(exitDone,
        dispatch_time(DISPATCH_TIME_NOW, (int64_t)(timeout * NSEC_PER_SEC)));
    if (timedOut != 0) {
        [task terminate];
        if (dispatch_semaphore_wait(exitDone,
                dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.0 * NSEC_PER_SEC))) != 0) {
            kill(task.processIdentifier, SIGKILL);
            [task waitUntilExit];
        }
        dispatch_semaphore_wait(readDone, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(2.0 * NSEC_PER_SEC)));
        if (output) *output = @"kernel command timed out";
        return -2;
    }
    dispatch_semaphore_wait(readDone, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(2.0 * NSEC_PER_SEC)));
    if (output) *output = data ? ([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding] ?: @"") : @"";
    return task.terminationStatus;
}

static int RunKernel(NSArray<NSString *> *arguments, NSString **output) {
    // A workspace on a stalled network/iCloud mount can block forever; every
    // shell-triggered kernel call needs a ceiling.
    return RunKernelTimeout(arguments, output, 20);
}

static NSString *ServerAddress(NSString *dir) {
    NSString *path = [dir stringByAppendingPathComponent:@"serve.json"];
    NSData *data = [NSData dataWithContentsOfFile:path];
    if (!data) return nil;
    NSDictionary *raw = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
    NSString *addr = [raw isKindOfClass:[NSDictionary class]] ? raw[@"addr"] : nil;
    return [addr isKindOfClass:[NSString class]] && addr.length ? addr : nil;
}

static NSString *ControlTokenFor(NSString *dir) {
    NSString *path = [dir stringByAppendingPathComponent:@"control-token"];
    NSString *token = [NSString stringWithContentsOfFile:path encoding:NSUTF8StringEncoding error:nil];
    token = [token stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]];
    return token.length ? token : nil;
}

// A dataless iCloud placeholder blocks the first read until it downloads. The
// window must not hang on that, so check before starting a server.
static BOOL WorkspaceNeedsDownload(NSString *dir) {
    NSFileManager *fm = [NSFileManager defaultManager];
    NSArray *probe = @[dir, [dir stringByAppendingPathComponent:@"kernel.db"]];
    for (NSString *path in probe) {
        if (![fm fileExistsAtPath:path]) continue;
        NSURL *url = [NSURL fileURLWithPath:path];
        id ubiquitous = nil;
        if (![url getResourceValue:&ubiquitous forKey:NSURLIsUbiquitousItemKey error:nil]) continue;
        if (![ubiquitous boolValue]) continue;
        id status = nil;
        [url getResourceValue:&status forKey:NSURLUbiquitousItemDownloadingStatusKey error:nil];
        if (status && ![status isEqualToString:NSURLUbiquitousItemDownloadingStatusCurrent]) return YES;
    }
    return NO;
}

static NSError *ICloudDownloadError(NSString *dir) {
    NSURL *url = [NSURL fileURLWithPath:[dir stringByAppendingPathComponent:@"kernel.db"]];
    id value = nil;
    [url getResourceValue:&value forKey:NSURLUbiquitousItemDownloadingErrorKey error:nil];
    return [value isKindOfClass:[NSError class]] ? value : nil;
}

// Ask iCloud to materialize the workspace. Directory-level requests do not
// always recurse, so also request the files a kernel needs to start.
static void RequestICloudDownload(NSString *dir) {
    NSFileManager *fm = [NSFileManager defaultManager];
    NSArray *names = @[@"", @"kernel.db", @"conversations.db", @"serve.json", @"control-token",
                       @"models.json", @"models-cache.json", @"projects"];
    for (NSString *name in names) {
        NSString *path = name.length ? [dir stringByAppendingPathComponent:name] : dir;
        if (![fm fileExistsAtPath:path]) continue;
        [fm startDownloadingUbiquitousItemAtURL:[NSURL fileURLWithPath:path] error:nil];
    }
    if ([fm isExecutableFileAtPath:@"/usr/bin/brctl"]) {
        NSTask *task = [[NSTask alloc] init];
        task.executableURL = [NSURL fileURLWithPath:@"/usr/bin/brctl"];
        task.arguments = @[@"download", dir];
        task.standardOutput = [NSFileHandle fileHandleWithNullDevice];
        task.standardError = [NSFileHandle fileHandleWithNullDevice];
        @try { [task launch]; } @catch (NSException *exception) {}
    }
}

static BOOL PortAvailable(int port) {
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return NO;
    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_port = htons((uint16_t)port);
    addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    BOOL freePort = bind(fd, (struct sockaddr *)&addr, sizeof(addr)) == 0;
    close(fd);
    return freePort;
}

// Ensures a live server for dir. Returns its base URL, or nil on failure.
// started is set to YES only when this call launched the server.
static NSString *EnsureServerFor(NSString *dir, BOOL *started) {
    [[NSFileManager defaultManager] createDirectoryAtPath:dir withIntermediateDirectories:YES attributes:nil error:nil];
    if (started) *started = NO;

    NSString *output = nil;
    int status = RunKernel(@[@"serve", @"--dir", dir, @"--status"], &output);
    if (status == 0 && [output containsString:@"alive=true"]) {
        NSString *existing = ServerAddress(dir);
        if (existing) return [@"http://" stringByAppendingString:existing];
    }

    for (int port = kDefaultPort; port < kDefaultPort + kMaxTabCount + 8; port++) {
        if (!PortAvailable(port)) continue;
        NSString *addr = [NSString stringWithFormat:@"127.0.0.1:%d", port];
        status = RunKernel(@[@"serve", @"--dir", dir, @"--control", @"--daemon", @"--addr", addr], &output);
        if (status != 0) continue;
        NSString *actual = ServerAddress(dir);
        if (!actual) continue;
        if (started && [output containsString:@"started pid="]) *started = YES;
        return [@"http://" stringByAppendingString:actual];
    }
    return nil;
}

static void StopServerFor(NSString *dir) {
    NSString *output = nil;
    RunKernel(@[@"serve", @"--dir", dir, @"--stop"], &output);
}

static NSString *CockpitURL(NSString *base, NSString *dir) {
    NSString *token = ControlTokenFor(dir);
    if (!token) return [base stringByAppendingString:@"/chat"];
    return [NSString stringWithFormat:@"%@/chat#token=%@", base, token];
}

static NSString *TabTitle(NSString *path) {
    NSString *last = [path lastPathComponent];
    return last.length ? last : path;
}

#pragma mark - Small views

@interface TabBarView : NSView
@end
@implementation TabBarView
- (void)drawRect:(NSRect)dirtyRect {
    [[NSColor controlBackgroundColor] setFill];
    NSRectFill(dirtyRect);
}
@end

@interface ChipView : NSView
@property(nonatomic) BOOL selected;
@end
@implementation ChipView
- (void)drawRect:(NSRect)dirtyRect {
    NSColor *fill = [NSColor clearColor];
    if (self.selected) fill = [NSColor.controlAccentColor colorWithAlphaComponent:0.14];
    [fill setFill];
    [[NSBezierPath bezierPathWithRoundedRect:self.bounds xRadius:7 yRadius:7] fill];
}
@end

#pragma mark - Workspace tab

typedef NS_ENUM(NSInteger, TabState) {
    TabStatePending,
    TabStateDownloading,
    TabStateStarting,
    TabStateReady,
    TabStateFailed,
};

@interface WorkspaceTab : NSObject
@property(nonatomic, copy) NSString *path;
@property(nonatomic, copy) NSString *baseURL;
@property(nonatomic, copy) NSString *identifier;
@property(nonatomic, strong) WKWebView *webView;
@property(nonatomic, strong) NSView *placeholder;
@property(nonatomic, strong) NSTextField *statusLabel;
@property(nonatomic, strong) NSTextField *detailLabel;
@property(nonatomic, strong) NSProgressIndicator *spinner;
@property(nonatomic, strong) NSButton *retryButton;
@property(nonatomic, strong) NSButton *finderButton;
@property(nonatomic) TabState state;
@property(nonatomic, copy) NSString *statusText;
@property(nonatomic) BOOL startedByApp;
@property(atomic) BOOL cancelled;
@end

@implementation WorkspaceTab
@end

#pragma mark - App delegate

@interface AppDelegate : NSObject <NSApplicationDelegate, WKUIDelegate>
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) NSView *tabBar;
@property(nonatomic, strong) NSView *content;
@property(nonatomic, strong) NSMutableArray<WorkspaceTab *> *tabs;
@property(nonatomic) NSInteger activeIndex;
@property(nonatomic) BOOL ephemeral; // PROJECTOS_DIR: one fixed workspace, no persistence
@property(nonatomic, strong) NSMutableSet<NSString *> *serversStarted;
@property(nonatomic, copy) NSString *dumpDir;
@property(nonatomic) NSInteger dumpWaited;
@property(nonatomic, strong) dispatch_queue_t startQueue;
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
    NSMenuItem *add = [[NSMenuItem alloc] initWithTitle:@"添加工作区标签…"
                                                 action:@selector(addWorkspace:)
                                          keyEquivalent:@"o"];
    add.target = self;
    [fileMenu addItem:add];
    NSMenuItem *close = [[NSMenuItem alloc] initWithTitle:@"关闭当前标签"
                                                   action:@selector(closeCurrentTab:)
                                            keyEquivalent:@"w"];
    close.target = self;
    [fileMenu addItem:close];

    NSMenuItem *tabItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:tabItem];
    NSMenu *tabMenu = [[NSMenu alloc] initWithTitle:@"标签"];
    [tabItem setSubmenu:tabMenu];
    NSArray *tabActions = @[
        @[@"下一个标签", @"nextTab:", @"]", @(NSEventModifierFlagCommand | NSEventModifierFlagShift)],
        @[@"上一个标签", @"previousTab:", @"[", @(NSEventModifierFlagCommand | NSEventModifierFlagShift)],
        @[@"重新加载当前标签", @"reloadCurrentTab:", @"r", @(NSEventModifierFlagCommand)],
    ];
    for (NSArray *entry in tabActions) {
        NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:entry[0]
                                                      action:NSSelectorFromString(entry[1])
                                               keyEquivalent:entry[2]];
        item.keyEquivalentModifierMask = [entry[3] unsignedIntegerValue];
        item.target = self;
        [tabMenu addItem:item];
    }

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

- (BOOL)validateMenuItem:(NSMenuItem *)item {
    if (item.action == @selector(addWorkspace:)) return !self.ephemeral && self.tabs.count < kMaxTabCount;
    if (item.action == @selector(closeCurrentTab:)) return self.tabs.count > 0;
    if (item.action == @selector(nextTab:) || item.action == @selector(previousTab:) ||
        item.action == @selector(reloadCurrentTab:)) return self.tabs.count > 0;
    return YES;
}

#pragma mark UI

- (void)buildWindow {
    NSRect frame = NSMakeRect(0, 0, 1280, 860);
    self.window = [[NSWindow alloc] initWithContentRect:frame
                                              styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                                                         NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable)
                                                backing:NSBackingStoreBuffered
                                                  defer:NO];
    self.window.minSize = NSMakeSize(900, 600);
    // The workspace UI is a light design; keep native chrome consistent even
    // when the system is in dark mode.
    self.window.appearance = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
    [self.window center];

    NSView *root = [[NSView alloc] initWithFrame:frame];
    self.tabBar = [[TabBarView alloc] initWithFrame:NSZeroRect];
    self.content = [[NSView alloc] initWithFrame:NSZeroRect];

    NSBox *separator = [[NSBox alloc] init];
    separator.boxType = NSBoxSeparator;
    separator.translatesAutoresizingMaskIntoConstraints = NO;
    [self.tabBar addSubview:separator];

    self.tabBar.translatesAutoresizingMaskIntoConstraints = NO;
    self.content.translatesAutoresizingMaskIntoConstraints = NO;
    [root addSubview:self.tabBar];
    [root addSubview:self.content];
    self.window.contentView = root;

    [NSLayoutConstraint activateConstraints:@[
        [self.tabBar.topAnchor constraintEqualToAnchor:root.topAnchor],
        [self.tabBar.leadingAnchor constraintEqualToAnchor:root.leadingAnchor],
        [self.tabBar.trailingAnchor constraintEqualToAnchor:root.trailingAnchor],
        [self.tabBar.heightAnchor constraintEqualToConstant:40],
        [separator.leadingAnchor constraintEqualToAnchor:self.tabBar.leadingAnchor],
        [separator.trailingAnchor constraintEqualToAnchor:self.tabBar.trailingAnchor],
        [separator.bottomAnchor constraintEqualToAnchor:self.tabBar.bottomAnchor],
        [self.content.topAnchor constraintEqualToAnchor:self.tabBar.bottomAnchor],
        [self.content.leadingAnchor constraintEqualToAnchor:root.leadingAnchor],
        [self.content.trailingAnchor constraintEqualToAnchor:root.trailingAnchor],
        [self.content.bottomAnchor constraintEqualToAnchor:root.bottomAnchor],
    ]];
    [self.window makeKeyAndOrderFront:nil];
}

- (NSButton *)flatButton:(NSString *)title action:(SEL)action tag:(NSInteger)tag {
    NSButton *button = [NSButton buttonWithTitle:title target:self action:action];
    button.bordered = NO;
    button.tag = tag;
    button.font = [NSFont systemFontOfSize:12 weight:NSFontWeightRegular];
    button.contentTintColor = [NSColor secondaryLabelColor];
    return button;
}

- (void)rebuildTabBar {
    for (NSView *view in [self.tabBar.subviews copy]) {
        if (![view isKindOfClass:[NSBox class]]) [view removeFromSuperview];
    }
    NSStackView *strip = [[NSStackView alloc] init];
    strip.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    strip.alignment = NSLayoutAttributeCenterY;
    strip.spacing = 4;
    strip.translatesAutoresizingMaskIntoConstraints = NO;
    [self.tabBar addSubview:strip];

    NSButton *plus = [self flatButton:@"＋" action:@selector(addWorkspace:) tag:-1];
    plus.font = [NSFont systemFontOfSize:14 weight:NSFontWeightRegular];
    plus.toolTip = @"添加工作区标签（⌘O）";
    plus.enabled = !self.ephemeral && self.tabs.count < kMaxTabCount;

    [strip.leadingAnchor constraintEqualToAnchor:self.tabBar.leadingAnchor constant:10].active = YES;
    [strip.trailingAnchor constraintLessThanOrEqualToAnchor:self.tabBar.trailingAnchor constant:-10].active = YES;
    [strip.centerYAnchor constraintEqualToAnchor:self.tabBar.centerYAnchor].active = YES;
    [plus.widthAnchor constraintEqualToConstant:26].active = YES;
    [plus.heightAnchor constraintEqualToConstant:24].active = YES;
    [strip addArrangedSubview:plus];

    for (NSInteger i = 0; i < (NSInteger)self.tabs.count; i++) {
        WorkspaceTab *tab = self.tabs[i];
        BOOL active = i == self.activeIndex;

        ChipView *chip = [[ChipView alloc] init];
        chip.selected = active;

        NSButton *title = [self flatButton:TabTitle(tab.path) action:@selector(selectTabFromButton:) tag:i];
        title.font = [NSFont systemFontOfSize:12 weight:active ? NSFontWeightMedium : NSFontWeightRegular];
        title.contentTintColor = active ? NSColor.controlAccentColor : NSColor.secondaryLabelColor;
        title.toolTip = tab.path;
        title.cell.lineBreakMode = NSLineBreakByTruncatingMiddle;
        [title.widthAnchor constraintLessThanOrEqualToConstant:180].active = YES;
        [title setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow
                                        forOrientation:NSLayoutConstraintOrientationHorizontal];

        NSButton *close = [self flatButton:@"×" action:@selector(closeTabFromButton:) tag:i];
        close.font = [NSFont systemFontOfSize:12 weight:NSFontWeightRegular];
        close.contentTintColor = NSColor.tertiaryLabelColor;
        close.toolTip = @"关闭标签（⌘W）";

        NSStackView *row = [NSStackView stackViewWithViews:@[title, close]];
        row.orientation = NSUserInterfaceLayoutOrientationHorizontal;
        row.alignment = NSLayoutAttributeCenterY;
        row.spacing = 2;
        row.edgeInsets = NSEdgeInsetsMake(0, 9, 0, 5);
        row.translatesAutoresizingMaskIntoConstraints = NO;
        [chip addSubview:row];
        [NSLayoutConstraint activateConstraints:@[
            [row.leadingAnchor constraintEqualToAnchor:chip.leadingAnchor],
            [row.trailingAnchor constraintEqualToAnchor:chip.trailingAnchor],
            [row.topAnchor constraintEqualToAnchor:chip.topAnchor],
            [row.bottomAnchor constraintEqualToAnchor:chip.bottomAnchor],
            [chip.heightAnchor constraintEqualToConstant:26],
        ]];
        [strip addArrangedSubview:chip];
    }
}

- (void)updateWindowChrome {
    if (!self.tabs.count) return;
    WorkspaceTab *tab = self.tabs[self.activeIndex];
    self.window.title = [NSString stringWithFormat:@"ProjectOS — %@", TabTitle(tab.path)];
    self.window.representedURL = [NSURL fileURLWithPath:tab.path];
}

- (void)applyVisibility {
    for (NSInteger i = 0; i < (NSInteger)self.tabs.count; i++) {
        WorkspaceTab *tab = self.tabs[i];
        BOOL active = i == self.activeIndex;
        BOOL ready = tab.state == TabStateReady && tab.webView != nil;
        if (tab.webView) tab.webView.hidden = !(active && ready);
        if (tab.placeholder) tab.placeholder.hidden = !(active && !ready);
    }
}

- (void)activateIndex:(NSInteger)index {
    if (!self.tabs.count) return;
    index = MAX(0, MIN(index, (NSInteger)self.tabs.count - 1));
    self.activeIndex = index;
    [self applyVisibility];
    [self rebuildTabBar];
    [self updateWindowChrome];
    [self persistState];
}

#pragma mark Tabs

- (WorkspaceTab *)tabForPath:(NSString *)path {
    NSString *standard = [path stringByStandardizingPath];
    for (WorkspaceTab *tab in self.tabs) {
        if ([[tab.path stringByStandardizingPath] isEqualToString:standard]) return tab;
    }
    return nil;
}

- (WorkspaceTab *)tabForIdentifier:(NSString *)identifier {
    for (WorkspaceTab *tab in self.tabs) {
        if ([tab.identifier isEqualToString:identifier]) return tab;
    }
    return nil;
}

- (NSView *)makePlaceholderForTab:(WorkspaceTab *)tab {
    NSView *box = [[NSView alloc] initWithFrame:NSZeroRect];
    box.translatesAutoresizingMaskIntoConstraints = NO;

    NSStackView *stack = [[NSStackView alloc] init];
    stack.orientation = NSUserInterfaceLayoutOrientationVertical;
    stack.alignment = NSLayoutAttributeCenterX;
    stack.spacing = 10;
    stack.translatesAutoresizingMaskIntoConstraints = NO;

    NSProgressIndicator *spinner = [[NSProgressIndicator alloc] init];
    spinner.style = NSProgressIndicatorStyleSpinning;
    spinner.controlSize = NSControlSizeSmall;
    spinner.displayedWhenStopped = NO;
    [spinner startAnimation:nil];

    NSTextField *title = [NSTextField labelWithString:TabTitle(tab.path)];
    title.font = [NSFont systemFontOfSize:15 weight:NSFontWeightSemibold];
    title.textColor = [NSColor labelColor];

    NSTextField *status = [NSTextField labelWithString:@"准备中…"];
    status.font = [NSFont systemFontOfSize:13];
    status.textColor = [NSColor secondaryLabelColor];
    status.alignment = NSTextAlignmentCenter;

    NSTextField *detail = [NSTextField wrappingLabelWithString:@""];
    detail.font = [NSFont systemFontOfSize:11.5];
    detail.textColor = [NSColor tertiaryLabelColor];
    detail.alignment = NSTextAlignmentCenter;
    detail.maximumNumberOfLines = 4;
    detail.preferredMaxLayoutWidth = 420;

    NSButton *retry = [NSButton buttonWithTitle:@"重试" target:self action:@selector(retryTabFromButton:)];
    NSButton *finder = [NSButton buttonWithTitle:@"在 Finder 中显示" target:self action:@selector(revealTabFromButton:)];
    retry.identifier = tab.identifier;
    finder.identifier = tab.identifier;
    retry.hidden = YES;
    finder.hidden = YES;
    NSStackView *row = [NSStackView stackViewWithViews:@[retry, finder]];
    row.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    row.spacing = 8;

    [stack addArrangedSubview:spinner];
    [stack addArrangedSubview:title];
    [stack addArrangedSubview:status];
    [stack addArrangedSubview:detail];
    [stack addArrangedSubview:row];
    [box addSubview:stack];
    [NSLayoutConstraint activateConstraints:@[
        [stack.centerXAnchor constraintEqualToAnchor:box.centerXAnchor],
        [stack.centerYAnchor constraintEqualToAnchor:box.centerYAnchor],
        [stack.leadingAnchor constraintGreaterThanOrEqualToAnchor:box.leadingAnchor constant:40],
        [stack.trailingAnchor constraintLessThanOrEqualToAnchor:box.trailingAnchor constant:-40],
    ]];

    tab.placeholder = box;
    tab.statusLabel = status;
    tab.detailLabel = detail;
    tab.spinner = spinner;
    tab.retryButton = retry;
    tab.finderButton = finder;
    return box;
}

- (WorkspaceTab *)createTabForPath:(NSString *)path {
    path = [path stringByStandardizingPath];
    WorkspaceTab *existing = [self tabForPath:path];
    if (existing) return existing;
    if (self.tabs.count >= kMaxTabCount) return nil;

    WorkspaceTab *tab = [[WorkspaceTab alloc] init];
    tab.path = path;
    tab.identifier = [[NSUUID UUID] UUIDString];
    tab.state = TabStatePending;
    tab.statusText = @"准备中…";

    NSView *placeholder = [self makePlaceholderForTab:tab];
    [self.content addSubview:placeholder];
    [NSLayoutConstraint activateConstraints:@[
        [placeholder.topAnchor constraintEqualToAnchor:self.content.topAnchor],
        [placeholder.leadingAnchor constraintEqualToAnchor:self.content.leadingAnchor],
        [placeholder.trailingAnchor constraintEqualToAnchor:self.content.trailingAnchor],
        [placeholder.bottomAnchor constraintEqualToAnchor:self.content.bottomAnchor],
    ]];
    placeholder.hidden = YES;
    [self.tabs addObject:tab];
    return tab;
}

- (void)applyTabState:(WorkspaceTab *)tab {
    if (!tab.placeholder) return;
    tab.statusLabel.stringValue = tab.statusText ?: @"";
    BOOL failed = tab.state == TabStateFailed;
    BOOL ready = tab.state == TabStateReady;
    tab.spinner.hidden = failed || ready;
    if (tab.spinner.hidden) [tab.spinner stopAnimation:nil];
    else [tab.spinner startAnimation:nil];
    tab.retryButton.hidden = !failed;
    tab.finderButton.hidden = !failed;
}

- (void)setTabState:(WorkspaceTab *)tab state:(TabState)state status:(NSString *)status detail:(NSString *)detail {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (tab.cancelled) return;
        tab.state = state;
        tab.statusText = status;
        if (tab.detailLabel) tab.detailLabel.stringValue = detail ?: @"";
        [self applyTabState:tab];
        [self applyVisibility];
    });
}

- (void)failTab:(WorkspaceTab *)tab message:(NSString *)message {
    [self setTabState:tab state:TabStateFailed status:@"无法打开工作区" detail:message];
}

- (void)prepareTab:(WorkspaceTab *)tab {
    tab.cancelled = NO;
    tab.state = TabStatePending;
    tab.statusText = @"准备中…";
    if (tab.detailLabel) tab.detailLabel.stringValue = @"";
    [self applyTabState:tab];
    [self applyVisibility];

    NSString *path = tab.path;
    dispatch_async(dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{
        if (WorkspaceNeedsDownload(path)) {
            [self setTabState:tab state:TabStateDownloading
                       status:@"正在下载 iCloud 文件…"
                       detail:@"文件还在 iCloud 中，已请求下载；完成后会自动启动工作区。"];
            RequestICloudDownload(path);

            NSDate *startedAt = [NSDate date];
            while (WorkspaceNeedsDownload(path)) {
                if (tab.cancelled) return;
                NSError *error = ICloudDownloadError(path);
                if (error) {
                    [self failTab:tab message:(error.localizedDescription ?: @"iCloud 下载失败。")];
                    return;
                }
                if ([[NSDate date] timeIntervalSinceDate:startedAt] > 900) {
                    [self failTab:tab message:@"iCloud 下载超过 15 分钟，请检查网络或在 Finder 中手动下载。"];
                    return;
                }
                [NSThread sleepForTimeInterval:0.5];
            }
        }

        if (tab.cancelled) return;
        [self setTabState:tab state:TabStateStarting
                   status:@"正在启动工作区…"
                   detail:@"文件已就绪，正在启动本地 Kernel。"];

        __block NSString *base = nil;
        __block BOOL started = NO;
        dispatch_sync(self.startQueue, ^{
            base = EnsureServerFor(path, &started);
            if (started) {
                @synchronized(self.serversStarted) {
                    [self.serversStarted addObject:[path stringByStandardizingPath]];
                }
            }
        });
        if (tab.cancelled) {
            if (started) StopServerFor(path);
            return;
        }
        dispatch_async(dispatch_get_main_queue(), ^{
            if (tab.cancelled) {
                if (started) StopServerFor(path);
                return;
            }
            if (!base) {
                [self failTab:tab message:@"无法启动本地 Kernel。检查 Resources/kernel、端口占用或 serve.log。"];
                return;
            }
            tab.baseURL = base;
            tab.startedByApp = started;
            [self attachWebViewForTab:tab];
        });
    });
}

- (void)attachWebViewForTab:(WorkspaceTab *)tab {
    if (tab.cancelled || !tab.baseURL) return;
    if (!tab.webView) {
        WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
        WKWebView *webView = [[WKWebView alloc] initWithFrame:self.content.bounds configuration:config];
        webView.UIDelegate = self;
        webView.translatesAutoresizingMaskIntoConstraints = NO;
        [self.content addSubview:webView];
        [NSLayoutConstraint activateConstraints:@[
            [webView.topAnchor constraintEqualToAnchor:self.content.topAnchor],
            [webView.leadingAnchor constraintEqualToAnchor:self.content.leadingAnchor],
            [webView.trailingAnchor constraintEqualToAnchor:self.content.trailingAnchor],
            [webView.bottomAnchor constraintEqualToAnchor:self.content.bottomAnchor],
        ]];
        tab.webView = webView;
    }
    tab.state = TabStateReady;
    tab.statusText = @"已就绪";
    [self applyTabState:tab];
    [tab.webView loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:CockpitURL(tab.baseURL, tab.path)]]];
    [self applyVisibility];
}

- (WorkspaceTab *)openTabForPath:(NSString *)path announce:(BOOL)announce {
    WorkspaceTab *existing = [self tabForPath:path];
    if (existing) return existing;
    WorkspaceTab *tab = [self createTabForPath:path];
    if (!tab) {
        if (announce) {
            NSAlert *alert = [[NSAlert alloc] init];
            alert.messageText = @"标签数量已达上限";
            alert.informativeText = [NSString stringWithFormat:@"最多同时打开 %d 个工作区。", kMaxTabCount];
            [alert runModal];
        }
        return nil;
    }
    [self prepareTab:tab];
    return tab;
}

- (void)retryTabFromButton:(NSButton *)sender {
    WorkspaceTab *tab = [self tabForIdentifier:sender.identifier];
    if (tab) [self prepareTab:tab];
}

- (void)revealTabFromButton:(NSButton *)sender {
    WorkspaceTab *tab = [self tabForIdentifier:sender.identifier];
    if (!tab) return;
    [[NSWorkspace sharedWorkspace] activateFileViewerSelectingURLs:@[[NSURL fileURLWithPath:tab.path]]];
}

- (void)addWorkspace:(id)sender {
    NSOpenPanel *panel = [NSOpenPanel openPanel];
    panel.canChooseDirectories = YES;
    panel.canChooseFiles = NO;
    panel.allowsMultipleSelection = NO;
    panel.canCreateDirectories = YES;
    panel.prompt = @"添加为标签";
    panel.message = @"选择工作区目录。每个标签运行独立的 Kernel，可并行推进不同项目。";
    if (self.tabs.count) panel.directoryURL = [NSURL fileURLWithPath:self.tabs[self.activeIndex].path];
    if ([panel runModal] != NSModalResponseOK) return;

    NSString *path = panel.URL.path;
    WorkspaceTab *existing = [self tabForPath:path];
    if (existing) {
        [self activateIndex:[self.tabs indexOfObject:existing]];
        return;
    }
    WorkspaceTab *tab = [self openTabForPath:path announce:YES];
    if (!tab) return;
    [self activateIndex:[self.tabs indexOfObject:tab]];
}

- (void)closeTabAtIndex:(NSInteger)index {
    if (index < 0 || index >= (NSInteger)self.tabs.count) return;
    WorkspaceTab *tab = self.tabs[index];
    NSString *standard = [tab.path stringByStandardizingPath];
    tab.cancelled = YES;
    [tab.webView removeFromSuperview];
    [tab.placeholder removeFromSuperview];
    [self.tabs removeObjectAtIndex:index];

    BOOL stillUsed = NO;
    for (WorkspaceTab *other in self.tabs) {
        if ([[other.path stringByStandardizingPath] isEqualToString:standard]) stillUsed = YES;
    }
    BOOL wasStarted = NO;
    @synchronized(self.serversStarted) {
        wasStarted = [self.serversStarted containsObject:standard];
    }
    if (!stillUsed && wasStarted) {
        if (self.dumpDir) { printf("stop-close %s\n", tab.path.UTF8String); fflush(stdout); }
        NSString *pathToStop = [tab.path copy];
        dispatch_async(self.startQueue, ^{
            StopServerFor(pathToStop);
            dispatch_async(dispatch_get_main_queue(), ^{
                @synchronized(self.serversStarted) {
                    [self.serversStarted removeObject:standard];
                }
            });
        });
    }

    if (!self.tabs.count) {
        [NSApp terminate:nil];
        return;
    }
    if (index < self.activeIndex) self.activeIndex -= 1;
    self.activeIndex = MIN(self.activeIndex, (NSInteger)self.tabs.count - 1);
    [self activateIndex:self.activeIndex];
}

- (void)selectTabFromButton:(NSButton *)sender {
    if (sender.tag < 0 || sender.tag >= (NSInteger)self.tabs.count) return;
    [self activateIndex:sender.tag];
}

- (void)closeTabFromButton:(NSButton *)sender {
    [self closeTabAtIndex:sender.tag];
}

- (void)closeCurrentTab:(id)sender {
    [self closeTabAtIndex:self.activeIndex];
}

- (void)nextTab:(id)sender {
    if (self.tabs.count < 2) return;
    [self activateIndex:(self.activeIndex + 1) % (NSInteger)self.tabs.count];
}

- (void)previousTab:(id)sender {
    if (self.tabs.count < 2) return;
    [self activateIndex:(self.activeIndex - 1 + (NSInteger)self.tabs.count) % (NSInteger)self.tabs.count];
}

- (void)reloadCurrentTab:(id)sender {
    if (!self.tabs.count) return;
    WorkspaceTab *tab = self.tabs[self.activeIndex];
    [tab.webView loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:CockpitURL(tab.baseURL, tab.path)]]];
}

#pragma mark Persistence

- (NSArray<NSString *> *)initialPaths {
    if (Env(@"PROJECTOS_DIR")) {
        self.ephemeral = YES;
        return @[[Env(@"PROJECTOS_DIR") stringByExpandingTildeInPath]];
    }
    NSUserDefaults *defaults = [NSUserDefaults standardUserDefaults];
    NSArray *saved = [defaults arrayForKey:kWorkspacePathsKey];
    NSMutableArray<NSString *> *paths = [NSMutableArray array];
    for (id entry in saved) {
        if (![entry isKindOfClass:[NSString class]] || ![entry length]) continue;
        NSString *path = [entry stringByExpandingTildeInPath];
        if ([paths containsObject:path]) continue;
        [paths addObject:path];
    }
    if (!paths.count) {
        NSString *legacy = [defaults stringForKey:kLegacyWorkspaceKey];
        if (legacy.length) [paths addObject:[legacy stringByExpandingTildeInPath]];
    }
    if (!paths.count) [paths addObject:DefaultStateDirectory()];
    return paths;
}

- (void)persistState {
    if (self.ephemeral || self.dumpDir) return;
    NSMutableArray<NSString *> *paths = [NSMutableArray array];
    for (WorkspaceTab *tab in self.tabs) [paths addObject:tab.path];
    NSUserDefaults *defaults = [NSUserDefaults standardUserDefaults];
    [defaults setObject:paths forKey:kWorkspacePathsKey];
    if (self.tabs.count) [defaults setObject:self.tabs[self.activeIndex].path forKey:kActiveWorkspaceKey];
    [defaults synchronize];
}

#pragma mark Lifecycle

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    [self installMenu];
    self.tabs = [NSMutableArray array];
    self.serversStarted = [NSMutableSet set];
    self.startQueue = dispatch_queue_create("dev.projectos.workspace-start", DISPATCH_QUEUE_SERIAL);
    [self buildWindow];

    NSArray<NSString *> *paths = [self initialPaths];
    NSString *activePath = self.ephemeral ? nil
        : [[NSUserDefaults standardUserDefaults] stringForKey:kActiveWorkspaceKey];
    // Create every tab and show the window before touching the network or a
    // server, so one slow workspace can never block the app from opening.
    for (NSString *path in paths) {
        [self createTabForPath:path];
    }
    if (!self.tabs.count) {
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"ProjectOS 无法启动";
        alert.informativeText = @"检查 kernel 二进制是否在 Resources 中，或设置 PROJECTOS_KERNEL。";
        [alert runModal];
        [NSApp terminate:nil];
        return;
    }
    NSInteger index = 0;
    if (activePath) {
        for (NSInteger i = 0; i < (NSInteger)self.tabs.count; i++) {
            if ([self.tabs[i].path isEqualToString:activePath]) { index = i; break; }
        }
    }
    [self activateIndex:index];
    [NSApp activateIgnoringOtherApps:YES];

    // The active workspace starts first; the rest prepare in parallel.
    if (index < (NSInteger)self.tabs.count) [self prepareTab:self.tabs[index]];
    for (NSInteger i = 0; i < (NSInteger)self.tabs.count; i++) {
        if (i != index) [self prepareTab:self.tabs[i]];
    }
    if (self.dumpDir) {
        [self performSelector:@selector(dumpTabsAndExit) withObject:nil afterDelay:6.0];
    }
}

// Test hook: prove that tabs exist, both pages are loaded, switching works, and
// render the native tab bar to a PNG without needing Screen Recording access.
- (void)dumpTabsAndExit {
    printf("tabs=%lu active=%ld appearance=%s bar=%s\n", (unsigned long)self.tabs.count,
           (long)self.activeIndex, self.window.effectiveAppearance.name.UTF8String,
           self.tabBar.effectiveAppearance.name.UTF8String);
    for (NSInteger i = 0; i < (NSInteger)self.tabs.count; i++) {
        WorkspaceTab *tab = self.tabs[i];
        printf("tab %ld state=%ld path=%s url=%s started=%d\n", (long)i, (long)tab.state,
               tab.path.UTF8String, tab.webView.URL.absoluteString.UTF8String ?: "", tab.startedByApp);
    }
    NSString *png = [self.dumpDir stringByAppendingPathComponent:@"tabbar.png"];
    NSBitmapImageRep *rep = [self.tabBar bitmapImageRepForCachingDisplayInRect:self.tabBar.bounds];
    [self.tabBar cacheDisplayInRect:self.tabBar.bounds toBitmapImageRep:rep];
    NSData *data = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
    [data writeToFile:png atomically:YES];
    if (self.tabs.count > 1) {
        [self activateIndex:(self.activeIndex + 1) % (NSInteger)self.tabs.count];
        printf("after-next active=%ld path=%s\n", (long)self.activeIndex,
               self.tabs[self.activeIndex].path.UTF8String);
    }
    if (self.tabs.count > 1) {
        [self closeTabAtIndex:0];
        printf("after-close tabs=%lu active=%ld path=%s\n", (unsigned long)self.tabs.count,
               (long)self.activeIndex, self.tabs[self.activeIndex].path.UTF8String);
    }
    fflush(stdout);
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(8.0 * NSEC_PER_SEC)),
                   dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender { return YES; }

- (void)webView:(WKWebView *)webView runJavaScriptAlertPanelWithMessage:(NSString *)message
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(void))completionHandler {
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = @"ProjectOS";
    alert.informativeText = message ?: @"";
    [alert addButtonWithTitle:@"好"];
    if (self.window) {
        [alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse response) {
            completionHandler();
        }];
    } else {
        [alert runModal];
        completionHandler();
    }
}

- (void)webView:(WKWebView *)webView runJavaScriptConfirmPanelWithMessage:(NSString *)message
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(BOOL))completionHandler {
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = @"请确认";
    alert.informativeText = message ?: @"";
    [alert addButtonWithTitle:@"取消"];
    [alert addButtonWithTitle:@"继续"];
    if (self.window) {
        [alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse response) {
            completionHandler(response == NSAlertSecondButtonReturn);
        }];
    } else {
        NSModalResponse response = [alert runModal];
        completionHandler(response == NSAlertSecondButtonReturn);
    }
}

- (void)webView:(WKWebView *)webView runJavaScriptTextInputPanelWithPrompt:(NSString *)prompt
    defaultText:(NSString *)defaultText initiatedByFrame:(WKFrameInfo *)frame
    completionHandler:(void (^)(NSString *))completionHandler {
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = @"ProjectOS";
    alert.informativeText = prompt ?: @"";
    [alert addButtonWithTitle:@"取消"];
    [alert addButtonWithTitle:@"确定"];
    NSTextField *field = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 320, 24)];
    field.stringValue = defaultText ?: @"";
    alert.accessoryView = field;
    NSModalResponse response = self.window
        ? [alert runModal]
        : [alert runModal];
    completionHandler(response == NSAlertSecondButtonReturn ? field.stringValue : nil);
}

- (void)applicationWillTerminate:(NSNotification *)notification {
    // Stop only servers this app started; an already-running daemon may serve
    // other clients or keep long-running objectives alive.
    NSArray<NSString *> *targets = nil;
    @synchronized(self.serversStarted) {
        targets = self.serversStarted.allObjects;
    }
    dispatch_group_t group = dispatch_group_create();
    for (NSString *standard in targets) {
        if (self.dumpDir) { printf("stop-quit %s\n", standard.UTF8String); fflush(stdout); }
        dispatch_group_enter(group);
        dispatch_async(dispatch_get_global_queue(QOS_CLASS_UTILITY, 0), ^{
            StopServerFor(standard);
            dispatch_group_leave(group);
        });
    }
    dispatch_group_wait(group, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(3.0 * NSEC_PER_SEC)));
    @synchronized(self.serversStarted) {
        [self.serversStarted removeAllObjects];
    }
}

@end

#pragma mark - main

static int CheckSingle(void) {
    BOOL started = NO;
    NSString *dir = Env(@"PROJECTOS_DIR");
    if (!dir) {
        NSArray *saved = [[NSUserDefaults standardUserDefaults] arrayForKey:kWorkspacePathsKey];
        if (saved.count && [saved[0] isKindOfClass:[NSString class]]) dir = saved[0];
    }
    if (!dir) dir = DefaultStateDirectory();
    NSString *base = EnsureServerFor(dir, &started);
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
    if (started) StopServerFor(dir);
    printf("%s %s\n", ok ? "ok" : "status request failed", base.UTF8String);
    return ok ? 0 : 1;
}

static int CheckMulti(NSString *dirA, NSString *dirB) {
    BOOL startedA = NO, startedB = NO;
    NSString *baseA = EnsureServerFor(dirA, &startedA);
    NSString *baseB = EnsureServerFor(dirB, &startedB);
    if (!baseA || !baseB) {
        fprintf(stderr, "one workspace failed to start (%s / %s)\n",
                baseA ? baseA.UTF8String : "nil", baseB ? baseB.UTF8String : "nil");
        return 1;
    }
    BOOL okA = NO, okB = NO;
    for (int i = 0; i < 40 && !(okA && okB); i++) {
        if (!okA) {
            NSData *data = [NSData dataWithContentsOfURL:[NSURL URLWithString:[baseA stringByAppendingString:@"/api/status"]]];
            okA = data.length > 0;
        }
        if (!okB) {
            NSData *data = [NSData dataWithContentsOfURL:[NSURL URLWithString:[baseB stringByAppendingString:@"/api/status"]]];
            okB = data.length > 0;
        }
        if (!(okA && okB)) [NSThread sleepForTimeInterval:0.25];
    }
    BOOL distinct = ![baseA isEqualToString:baseB];
    if (startedA) StopServerFor(dirA);
    if (startedB) StopServerFor(dirB);
    BOOL ok = okA && okB && distinct;
    printf("%s %s %s distinct=%d\n", ok ? "ok" : "failed",
           baseA.UTF8String, baseB.UTF8String, distinct);
    return ok ? 0 : 1;
}

static NSInteger HTTPJSON(NSString *url, NSString *method, NSString *token, NSString *body, NSData **out) {
    NSMutableURLRequest *request = [NSMutableURLRequest requestWithURL:[NSURL URLWithString:url]];
    request.HTTPMethod = method;
    if (token) [request setValue:token forHTTPHeaderField:@"X-Kernel-Token"];
    if (body) {
        [request setValue:@"application/json" forHTTPHeaderField:@"Content-Type"];
        request.HTTPBody = [body dataUsingEncoding:NSUTF8StringEncoding];
    }
    __block NSInteger status = 0;
    __block NSData *data = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [[[NSURLSession sharedSession] dataTaskWithRequest:request
                                    completionHandler:^(NSData *d, NSURLResponse *r, NSError *e) {
        data = d;
        if ([r isKindOfClass:[NSHTTPURLResponse class]]) status = [(NSHTTPURLResponse *)r statusCode];
        dispatch_semaphore_signal(sem);
    }] resume];
    if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 90 * NSEC_PER_SEC)) != 0) return -1;
    if (out) *out = data;
    return status;
}

// Proves that a kernel launched by the app (even with a minimal GUI PATH) can
// reach OpenCode and fill the model cache.
static int CheckModels(NSString *dir) {
    BOOL started = NO;
    NSString *base = EnsureServerFor(dir, &started);
    if (!base) {
        fprintf(stderr, "server did not start\n");
        return 1;
    }
    NSString *token = ControlTokenFor(dir);
    NSData *ignored = nil;
    NSInteger post = HTTPJSON([base stringByAppendingString:@"/api/models"], @"POST", token,
                              @"{\"refresh\":true}", &ignored);
    NSData *data = nil;
    NSInteger get = HTTPJSON([base stringByAppendingString:@"/api/models"], @"GET", token, nil, &data);
    NSInteger count = -1;
    if (data) {
        id obj = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
        if ([obj isKindOfClass:[NSDictionary class]]) {
            id models = obj[@"models"];
            if ([models isKindOfClass:[NSArray class]]) count = (NSInteger)[(NSArray *)models count];
        }
    }
    if (started) StopServerFor(dir);
    BOOL ok = post / 100 == 2 && get == 200 && count > 0;
    printf("%s refresh=%ld get=%ld models=%ld\n", ok ? "ok" : "failed",
           (long)post, (long)get, (long)count);
    return ok ? 0 : 1;
}

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        BOOL check = NO;
        NSString *multiA = nil, *multiB = nil, *dumpDir = nil, *modelsDir = nil, *inspectDir = nil;
        for (int i = 1; i < argc; i++) {
            if (strcmp(argv[i], "--check") == 0) check = YES;
            if (strcmp(argv[i], "--check-models") == 0 && i + 1 < argc) {
                modelsDir = [NSString stringWithUTF8String:argv[i + 1]];
            }
            if (strcmp(argv[i], "--check-workspace") == 0 && i + 1 < argc) {
                inspectDir = [NSString stringWithUTF8String:argv[i + 1]];
            }
            if (strcmp(argv[i], "--check-multi") == 0 && i + 2 < argc) {
                multiA = [NSString stringWithUTF8String:argv[i + 1]];
                multiB = [NSString stringWithUTF8String:argv[i + 2]];
            }
            if (strcmp(argv[i], "--dump-tabs") == 0 && i + 1 < argc) {
                dumpDir = [NSString stringWithUTF8String:argv[i + 1]];
            }
        }
        if (inspectDir) {
            printf("%s needsDownload=%d\n", inspectDir.UTF8String,
                   WorkspaceNeedsDownload(inspectDir) ? 1 : 0);
            return 0;
        }
        if (modelsDir) return CheckModels(modelsDir);
        if (multiA && multiB) return CheckMulti(multiA, multiB);
        if (check) return CheckSingle();

        NSApplication *app = [NSApplication sharedApplication];
        AppDelegate *delegate = [[AppDelegate alloc] init];
        if (dumpDir) {
            [[NSFileManager defaultManager] createDirectoryAtPath:dumpDir
                                      withIntermediateDirectories:YES attributes:nil error:nil];
            delegate.dumpDir = dumpDir;
        }
        app.delegate = delegate;
        [app setActivationPolicy:NSApplicationActivationPolicyRegular];
        [app run];
    }
    return 0;
}
