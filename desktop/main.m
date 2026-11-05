#import <Cocoa/Cocoa.h>
#import <string.h>
#import <WebKit/WebKit.h>

// A thin native window around the local cockpit. The kernel still serves the
// same single-page app; this shell only starts it, shows it, and stops the HTTP
// client on quit. Workers run in detached sessions, so quitting does not abort
// an in-flight objective.

static NSString *const kAppName = @"ProjectOS";
static const int kDefaultPort = 8799;
static BOOL gStartedServer = NO;

static NSString *Env(NSString *key) {
    NSString *value = [[[NSProcessInfo processInfo] environment] objectForKey:key];
    value = [value stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceAndNewlineCharacterSet]];
    return value.length ? value : nil;
}

static NSString *StateDirectory(void) {
    NSString *override = Env(@"PROJECTOS_DIR");
    if (override) return override;
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

static void StopServer(void) {
    NSString *output = nil;
    RunKernel(@[@"serve", @"--dir", StateDirectory(), @"--stop"], &output);
}

@interface AppDelegate : NSObject <NSApplicationDelegate>
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) WKWebView *webView;
@end

@implementation AppDelegate

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
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
    self.window.title = kAppName;
    [self.window center];
    self.window.contentView = self.webView;
    [self.window makeKeyAndOrderFront:nil];
    // Put the control token in the fragment so the cockpit is usable without
    // asking the user to open control-token in a terminal. Fragments never reach
    // the server and the page removes it from the address bar after storing it.
    NSString *token = ControlToken();
    NSString *url = token ? [NSString stringWithFormat:@"%@/#token=%@", base, token] : base;
    [self.webView loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:url]]];
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
