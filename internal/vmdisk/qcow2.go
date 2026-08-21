package vmdisk

import (
	"fmt"
	"os"
)

// qcow2LogicalDisk walks the two-level (L1/L2) cluster-mapping table
// QCOW2 uses. Compressed clusters are deliberately not decompressed -
// ReadLogical returns ErrCannotMapLogicalDisk if the region requested
// falls in one, rather than guessing at their content.
type qcow2LogicalDisk struct {
	file          *os.File
	l1TableOffset int64
	l1Size        uint32
	clusterBits   uint32
	clusterSize   int64
	l2Entries     int64 // clusterSize / 8
	fileSize      int64
}

const (
	qcow2OFlagCopied     = uint64(1) << 63
	qcow2OFlagCompressed = uint64(1) << 62
	qcow2OffsetMask      = (uint64(1) << 56) - 1
)

func newQCOW2LogicalDisk(file *os.File) (*qcow2LogicalDisk, error) {
	header, err := readAtBounded(file, 0, 72)
	if err != nil {
		return nil, fmt.Errorf("vmdisk: read qcow2 header: %w", err)
	}
	clusterBits := be32(header[20:24])
	l1Size := be32(header[36:40])
	l1TableOffset := int64(be64(header[40:48]))
	if clusterBits < 9 || clusterBits > 21 {
		return nil, fmt.Errorf("%w: implausible qcow2 cluster_bits=%d", ErrCannotMapLogicalDisk, clusterBits)
	}
	if l1Size > (1 << 24) {
		return nil, fmt.Errorf("%w: implausible qcow2 l1_size=%d", ErrCannotMapLogicalDisk, l1Size)
	}
	clusterSize := int64(1) << clusterBits
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("vmdisk: stat qcow2: %w", err)
	}
	l1Bytes := int64(l1Size) * 8
	if l1TableOffset < 72 || l1TableOffset%clusterSize != 0 || l1Bytes < 0 || l1TableOffset > stat.Size()-l1Bytes {
		return nil, fmt.Errorf("%w: qcow2 L1 table is unaligned, truncated, or outside the image", ErrCannotMapLogicalDisk)
	}
	return &qcow2LogicalDisk{
		file: file, l1TableOffset: l1TableOffset, l1Size: l1Size,
		clusterBits: clusterBits, clusterSize: clusterSize, l2Entries: clusterSize / 8, fileSize: stat.Size(),
	}, nil
}

func (q *qcow2LogicalDisk) ReadLogical(offset, length int64) ([]byte, error) {
	out := make([]byte, 0, length)
	for remaining := length; remaining > 0; {
		clusterIndex := offset / q.clusterSize
		offsetInCluster := offset % q.clusterSize
		chunk := q.clusterSize - offsetInCluster
		if chunk > remaining {
			chunk = remaining
		}

		l1Index := clusterIndex / q.l2Entries
		if l1Index < 0 || uint32(l1Index) >= q.l1Size {
			return nil, fmt.Errorf("vmdisk: qcow2 logical offset %d beyond L1 table (index %d >= %d)", offset, l1Index, q.l1Size)
		}
		l1EntryBytes, err := readAtBounded(q.file, q.l1TableOffset+l1Index*8, 8)
		if err != nil {
			return nil, err
		}
		l2TableOffset := int64(be64(l1EntryBytes) & qcow2OffsetMask)

		var data []byte
		if l2TableOffset == 0 {
			data = make([]byte, chunk) // whole L2 table unallocated: zero
		} else {
			if l2TableOffset%q.clusterSize != 0 || l2TableOffset < 72 || l2TableOffset > q.fileSize-q.clusterSize ||
				regionsOverlap(l2TableOffset, q.clusterSize, q.l1TableOffset, int64(q.l1Size)*8) {
				return nil, fmt.Errorf("%w: qcow2 L2 table at offset %d is unaligned, truncated, or overlaps metadata", ErrCannotMapLogicalDisk, l2TableOffset)
			}
			l2Index := clusterIndex % q.l2Entries
			l2EntryBytes, err := readAtBounded(q.file, l2TableOffset+l2Index*8, 8)
			if err != nil {
				return nil, err
			}
			l2Entry := be64(l2EntryBytes)
			switch {
			case l2Entry&qcow2OFlagCompressed != 0:
				return nil, fmt.Errorf("%w: qcow2 cluster at logical offset %d is compressed", ErrCannotMapLogicalDisk, offset)
			case l2Entry&qcow2OffsetMask == 0:
				data = make([]byte, chunk) // unallocated cluster: zero
			default:
				hostOffset := int64(l2Entry & qcow2OffsetMask)
				if hostOffset%q.clusterSize != 0 || hostOffset < 72 || hostOffset > q.fileSize-q.clusterSize ||
					regionsOverlap(hostOffset, q.clusterSize, q.l1TableOffset, int64(q.l1Size)*8) ||
					regionsOverlap(hostOffset, q.clusterSize, l2TableOffset, q.clusterSize) {
					return nil, fmt.Errorf("%w: qcow2 data cluster at offset %d is unaligned, truncated, or overlaps metadata", ErrCannotMapLogicalDisk, hostOffset)
				}
				data, err = readAtBounded(q.file, hostOffset+offsetInCluster, chunk)
				if err != nil {
					return nil, err
				}
			}
		}
		out = append(out, data...)
		offset += chunk
		remaining -= chunk
	}
	return out, nil
}

func regionsOverlap(aStart, aLength, bStart, bLength int64) bool {
	return aLength > 0 && bLength > 0 && aStart < bStart+bLength && bStart < aStart+aLength
}
