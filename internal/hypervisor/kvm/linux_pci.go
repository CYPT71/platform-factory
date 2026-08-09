package kvm

const (
	pciConfigAddressPort = uint16(0xcf8)
	pciConfigDataPort    = uint16(0xcfc)
)

// handleLinuxPCIConfigIO stands in for the PCI configuration mechanism #1
// ports (CONFIG_ADDRESS/CONFIG_DATA). This VMM has no PCI host bridge, so
// leaving these ports unhandled would let the guest read back whatever bytes
// were last left in the shared kvm_run mapping by an unrelated I/O exit.
// arch/x86/pci/direct.c's pci_check_direct() probes for the mechanism by
// writing a sentinel to CONFIG_ADDRESS and reading it back; stale, unrelated
// bytes can coincidentally match that sentinel, which fools Linux into
// believing a PCI bus exists and sending it off enumerating phantom devices
// built from more uninitialized bytes - the observed cause of an
// intermittent, unbounded-feeling stall deep in PCI/legacy driver probing.
// Never echoing CONFIG_ADDRESS back keeps that probe reliably negative, and
// CONFIG_DATA always reads as all-ones (the standard "nothing answered"
// value), so PCI enumeration is skipped outright instead of leaving the
// outcome to chance.
func handleLinuxPCIConfigIO(direction byte, size uint64, port uint16, data []byte) (bool, error) {
	if port != pciConfigAddressPort && port != pciConfigDataPort {
		return false, nil
	}
	if direction != linuxIODirectionOut {
		for index := range data {
			data[index] = 0xff
		}
	}
	return true, nil
}
