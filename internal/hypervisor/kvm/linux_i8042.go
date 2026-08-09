package kvm

import "errors"

const (
	i8042DataPort    = uint16(0x60)
	i8042CommandPort = uint16(0x64)
	i8042Reset       = byte(0xfe)
)

var errLinuxGuestReset = errors.New("vmm: kvm: Linux guest requested reset through i8042")

// handleLinuxI8042IO implements only the legacy reads Linux needs during boot
// and the reset command used during reboot. It deliberately does not claim to
// emulate a keyboard controller. Non-byte and unrelated accesses are left to
// the caller so another device handler can process or reject them.
func handleLinuxI8042IO(direction byte, size uint64, port uint16, data []byte) (bool, error) {
	if size != 1 || (port != i8042DataPort && port != i8042CommandPort) {
		return false, nil
	}
	if direction != linuxIODirectionOut {
		// Both the status and data reads return a neutral value: no output is
		// pending and the controller is ready for another command.
		clear(data)
		return true, nil
	}
	if port == i8042CommandPort {
		for _, value := range data {
			if value == i8042Reset {
				return true, errLinuxGuestReset
			}
		}
	}
	return true, nil
}
