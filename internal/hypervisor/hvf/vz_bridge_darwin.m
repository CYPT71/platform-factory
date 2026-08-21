// See vz_bridge_darwin.h for the contract this file implements. Compiled
// under ARC (see the #cgo CFLAGS in vmm_darwin.go); vz_machine_t is an
// opaque C alias for a retained VZBridgeMachine, moved across the ARC/C
// boundary with __bridge_retained/__bridge/__bridge_transfer so the struct
// itself never needs a full C definition.
#import <Foundation/Foundation.h>
#import <Virtualization/Virtualization.h>
#include <fcntl.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>
#include "vz_bridge_darwin.h"

// No framework call in this file is allowed to hang the calling OS thread
// forever: a stuck completion handler (missing entitlement, framework
// internal fault) must surface as an error, not a permanently blocked
// goroutine.
static const int64_t kBridgeWatchdogSeconds = 60;

@interface VZBridgeMachine : NSObject
@property(strong) VZVirtualMachine *vm;
@property(strong) dispatch_queue_t queue;
@property(strong) NSFileHandle *serialLogHandle;
@property(strong) NSFileHandle *guestAgentHandle;
@end

@implementation VZBridgeMachine
@end

static void set_error(const char *message, char *error_out, size_t error_cap) {
    if (error_out == NULL || error_cap == 0) {
        return;
    }
    if (message == NULL) {
        message = "unknown error";
    }
    strncpy(error_out, message, error_cap - 1);
    error_out[error_cap - 1] = '\0';
}

static void copy_error(NSError *error, char *error_out, size_t error_cap) {
    set_error(error != nil ? error.localizedDescription.UTF8String : "unknown error", error_out, error_cap);
}

int vz_is_supported(void) {
    return VZVirtualMachine.isSupported ? 1 : 0;
}

int vz_linux_rosetta_is_supported(void) {
    if (@available(macOS 13.0, *)) {
        return VZLinuxRosettaDirectoryShare.availability != VZLinuxRosettaAvailabilityNotSupported;
    }
    return 0;
}

int vz_linux_rosetta_is_installed(void) {
    if (@available(macOS 13.0, *)) {
        return VZLinuxRosettaDirectoryShare.availability == VZLinuxRosettaAvailabilityInstalled;
    }
    return 0;
}

vz_machine_t *vz_create_machine(
    const char *kernel_path, const char *initrd_path, const char *command_line,
    const char *serial_log_path, unsigned long long memory_bytes, unsigned int vcpu_count,
    const char *mac_address, int enable_linux_rosetta, int *guest_agent_fd_out,
    char *error_out, size_t error_cap) {
    @autoreleasepool {
        if (guest_agent_fd_out != NULL) {
            *guest_agent_fd_out = -1;
        }
        if (kernel_path == NULL || serial_log_path == NULL) {
            set_error("kernel_path and serial_log_path are required", error_out, error_cap);
            return NULL;
        }
        NSURL *kernelURL = [NSURL fileURLWithPath:[NSString stringWithUTF8String:kernel_path]];
        VZLinuxBootLoader *bootLoader = [[VZLinuxBootLoader alloc] initWithKernelURL:kernelURL];
        if (initrd_path != NULL) {
            bootLoader.initialRamdiskURL = [NSURL fileURLWithPath:[NSString stringWithUTF8String:initrd_path]];
        }
        if (command_line != NULL) {
            bootLoader.commandLine = [NSString stringWithUTF8String:command_line];
        }

        NSFileHandle *logHandle = [NSFileHandle fileHandleForWritingAtPath:[NSString stringWithUTF8String:serial_log_path]];
        if (logHandle == nil) {
            [[NSFileManager defaultManager] createFileAtPath:[NSString stringWithUTF8String:serial_log_path] contents:nil attributes:nil];
            logHandle = [NSFileHandle fileHandleForWritingAtPath:[NSString stringWithUTF8String:serial_log_path]];
        }
        if (logHandle == nil) {
            set_error("could not open serial_log_path for writing", error_out, error_cap);
            return NULL;
        }
        VZVirtioConsoleDeviceSerialPortConfiguration *serialConfig = [[VZVirtioConsoleDeviceSerialPortConfiguration alloc] init];
        serialConfig.attachment = [[VZFileHandleSerialPortAttachment alloc] initWithFileHandleForReading:nil fileHandleForWriting:logHandle];
        NSMutableArray<VZSerialPortConfiguration *> *serialPorts = [NSMutableArray arrayWithObject:serialConfig];
        NSFileHandle *guestAgentHandle = nil;
        int guestAgentHostFD = -1;
        if (guest_agent_fd_out != NULL) {
            int channel[2] = {-1, -1};
            if (socketpair(AF_UNIX, SOCK_STREAM, 0, channel) != 0) {
                set_error("could not create guest agent socketpair", error_out, error_cap);
                return NULL;
            }
            fcntl(channel[0], F_SETFD, fcntl(channel[0], F_GETFD) | FD_CLOEXEC);
            fcntl(channel[1], F_SETFD, fcntl(channel[1], F_GETFD) | FD_CLOEXEC);
            guestAgentHostFD = channel[0];
            guestAgentHandle = [[NSFileHandle alloc] initWithFileDescriptor:channel[1] closeOnDealloc:YES];
            VZVirtioConsoleDeviceSerialPortConfiguration *guestAgentConfig =
                [[VZVirtioConsoleDeviceSerialPortConfiguration alloc] init];
            guestAgentConfig.attachment = [[VZFileHandleSerialPortAttachment alloc]
                initWithFileHandleForReading:guestAgentHandle fileHandleForWriting:guestAgentHandle];
            [serialPorts addObject:guestAgentConfig];
        }

        VZVirtioEntropyDeviceConfiguration *entropy = [[VZVirtioEntropyDeviceConfiguration alloc] init];

        VZVirtualMachineConfiguration *configuration = [[VZVirtualMachineConfiguration alloc] init];
        configuration.bootLoader = bootLoader;
        configuration.platform = [[VZGenericPlatformConfiguration alloc] init];
        configuration.memorySize = memory_bytes;
        configuration.CPUCount = vcpu_count;
        configuration.serialPorts = serialPorts;
        configuration.entropyDevices = @[ entropy ];

        if (enable_linux_rosetta != 0) {
            if (@available(macOS 13.0, *)) {
                if (VZLinuxRosettaDirectoryShare.availability != VZLinuxRosettaAvailabilityInstalled) {
                    set_error("Rosetta for Linux is not installed", error_out, error_cap);
                    if (guestAgentHostFD >= 0) {
                        close(guestAgentHostFD);
                    }
                    [guestAgentHandle closeFile];
                    return NULL;
                }
                NSError *rosettaError = nil;
                VZLinuxRosettaDirectoryShare *share = [[VZLinuxRosettaDirectoryShare alloc] initWithError:&rosettaError];
                if (share == nil) {
                    copy_error(rosettaError, error_out, error_cap);
                    if (guestAgentHostFD >= 0) {
                        close(guestAgentHostFD);
                    }
                    [guestAgentHandle closeFile];
                    return NULL;
                }
                VZVirtioFileSystemDeviceConfiguration *fileSystem =
                    [[VZVirtioFileSystemDeviceConfiguration alloc] initWithTag:@"rosetta"];
                fileSystem.share = share;
                configuration.directorySharingDevices = @[ fileSystem ];
            } else {
                set_error("Rosetta for Linux requires macOS 13 or newer", error_out, error_cap);
                if (guestAgentHostFD >= 0) {
                    close(guestAgentHostFD);
                }
                [guestAgentHandle closeFile];
                return NULL;
            }
        }

        // See vz_bridge_darwin.h: NAT-attached virtio-net, guest negotiates
        // its own address (this project's kernel does that via `ip=dhcp`,
        // no userspace DHCP client needed in the guest).
        if (mac_address != NULL) {
            VZMACAddress *macAddress = [[VZMACAddress alloc] initWithString:[NSString stringWithUTF8String:mac_address]];
            if (macAddress == nil) {
                set_error("invalid mac_address", error_out, error_cap);
                if (guestAgentHostFD >= 0) {
                    close(guestAgentHostFD);
                }
                [guestAgentHandle closeFile];
                return NULL;
            }
            VZVirtioNetworkDeviceConfiguration *networkConfig = [[VZVirtioNetworkDeviceConfiguration alloc] init];
            networkConfig.MACAddress = macAddress;
            networkConfig.attachment = [[VZNATNetworkDeviceAttachment alloc] init];
            configuration.networkDevices = @[ networkConfig ];
        }

        NSError *validationError = nil;
        if (![configuration validateWithError:&validationError]) {
            if (guestAgentHostFD >= 0) {
                close(guestAgentHostFD);
            }
            [guestAgentHandle closeFile];
            copy_error(validationError, error_out, error_cap);
            return NULL;
        }

        dispatch_queue_t queue = dispatch_queue_create("dev.platform-factory.vmm", DISPATCH_QUEUE_SERIAL);
        VZBridgeMachine *bridge = [[VZBridgeMachine alloc] init];
        bridge.queue = queue;
        bridge.serialLogHandle = logHandle;
        bridge.guestAgentHandle = guestAgentHandle;
        // initWithConfiguration:queue: must itself run on `queue` per
        // Apple's documented contract for the designated initializer.
        dispatch_sync(queue, ^{
            bridge.vm = [[VZVirtualMachine alloc] initWithConfiguration:configuration queue:queue];
        });
        if (guest_agent_fd_out != NULL) {
            *guest_agent_fd_out = guestAgentHostFD;
        }
        return (__bridge_retained vz_machine_t *)bridge;
    }
}

int vz_machine_start(vz_machine_t *machine, char *error_out, size_t error_cap) {
    @autoreleasepool {
        VZBridgeMachine *bridge = (__bridge VZBridgeMachine *)machine;
        __block NSError *resultError = nil;
        dispatch_semaphore_t done = dispatch_semaphore_create(0);
        dispatch_async(bridge.queue, ^{
            [bridge.vm startWithCompletionHandler:^(NSError *_Nullable errorOrNil) {
                resultError = errorOrNil;
                dispatch_semaphore_signal(done);
            }];
        });
        long timedOut = dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, kBridgeWatchdogSeconds * NSEC_PER_SEC));
        if (timedOut != 0) {
            set_error("start did not complete within the watchdog timeout", error_out, error_cap);
            return 1;
        }
        if (resultError != nil) {
            copy_error(resultError, error_out, error_cap);
            return 1;
        }
        return 0;
    }
}

int vz_machine_stop(vz_machine_t *machine, char *error_out, size_t error_cap) {
    @autoreleasepool {
        VZBridgeMachine *bridge = (__bridge VZBridgeMachine *)machine;
        __block NSError *resultError = nil;
        dispatch_semaphore_t done = dispatch_semaphore_create(0);
        dispatch_async(bridge.queue, ^{
            if (bridge.vm.state == VZVirtualMachineStateStopped) {
                dispatch_semaphore_signal(done);
                return;
            }
            if (!bridge.vm.canStop) {
                resultError = [NSError errorWithDomain:@"dev.platform-factory.vmm" code:1
                                               userInfo:@{NSLocalizedDescriptionKey : @"machine is not in a stoppable state"}];
                dispatch_semaphore_signal(done);
                return;
            }
            [bridge.vm stopWithCompletionHandler:^(NSError *_Nullable errorOrNil) {
                resultError = errorOrNil;
                dispatch_semaphore_signal(done);
            }];
        });
        long timedOut = dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, kBridgeWatchdogSeconds * NSEC_PER_SEC));
        if (timedOut != 0) {
            set_error("stop did not complete within the watchdog timeout", error_out, error_cap);
            return 1;
        }
        if (resultError != nil) {
            copy_error(resultError, error_out, error_cap);
            return 1;
        }
        return 0;
    }
}

int vz_machine_state(vz_machine_t *machine) {
    VZBridgeMachine *bridge = (__bridge VZBridgeMachine *)machine;
    __block int state = (int)VZVirtualMachineStateError;
    dispatch_sync(bridge.queue, ^{
        state = (int)bridge.vm.state;
    });
    return state;
}

void vz_machine_free(vz_machine_t *machine) {
    VZBridgeMachine *bridge = (__bridge_transfer VZBridgeMachine *)machine;
    dispatch_sync(bridge.queue, ^{
        [bridge.serialLogHandle closeFile];
        [bridge.guestAgentHandle closeFile];
        bridge.vm = nil;
    });
}
