package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// payloadSizes spans the range this project cares about: from a payload
// too small to hide fixed per-build overhead, up to a size squarely
// outside normal single-binary use, so a throughput regression at scale
// shows up before it reaches production.
var payloadSizes = []int{
	1 << 10, 4 << 10, 64 << 10, 256 << 10,
	1 << 20, 4 << 20, 16 << 20, 32 << 20,
}

// syntheticPayload is deterministic (fixed seed) but not trivially
// compressible - it stands in for a real compiled binary, not a run of
// zeroes gzip would shrink for free. Kept out of any timed section: it
// runs once per size before b.ResetTimer, not once per b.N iteration.
func syntheticPayload(size int) []byte {
	payload := make([]byte, size)
	rand.New(rand.NewSource(1)).Read(payload) //nolint:gosec // deterministic test fixture, not a security use.
	return payload
}

// BenchmarkBuild measures the full, real path an operator actually takes:
// cmd/oci-builder's Build() - input validation and ELF closure checks,
// reading the whole binary into memory, building a deterministic tar
// layer, gzip-compressing it, marshaling the config/manifest/index JSON,
// then writing every blob to a fresh temp directory on disk and
// atomically renaming it into place. This is the end-to-end number: CPU
// (hashing, compression), memory (buffers held across those steps), and
// disk I/O (MkdirTemp, WriteFile per blob, Rename) are all included, with
// no fsync anywhere in this path.
func BenchmarkBuild(b *testing.B) {
	for _, size := range payloadSizes {
		b.Run(fmt.Sprintf("payload_%dKiB", size>>10), func(b *testing.B) {
			root := b.TempDir()
			binary := filepath.Join(root, "service")
			if err := os.WriteFile(binary, syntheticPayload(size), 0o755); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				output := filepath.Join(root, fmt.Sprintf("layout-%d", i))
				if _, err := Build(Options{
					Binary:       binary,
					Output:       output,
					Architecture: "amd64",
					Created:      time.Unix(0, 0),
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMakeLayer isolates the in-memory CPU/memory cost - tar framing,
// SHA-256, and gzip compression - with no disk I/O at all (unlike
// BenchmarkBuild, which also pays for MkdirTemp/WriteFile/Rename). The gap
// between this and BenchmarkBuild's per-byte cost is approximately what
// disk I/O adds on the benchmark machine's temp filesystem.
func BenchmarkMakeLayer(b *testing.B) {
	for _, size := range payloadSizes {
		b.Run(fmt.Sprintf("payload_%dKiB", size>>10), func(b *testing.B) {
			data := syntheticPayload(size)
			files := []streamFile{newInlineStreamFile("app/service", data, 0555)}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := writeLayerStream(io.Discard, files, nil, gzip.BestCompression, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkBuildParallel measures concurrent Build() calls at fixed
// concurrency levels, each into its own output directory (Build refuses
// to overwrite an existing path, so this also exercises that every
// concurrent build gets a genuinely independent temp directory and
// rename target without cross-goroutine interference).
func BenchmarkBuildParallel(b *testing.B) {
	const size = 1 << 20 // 1 MiB: large enough to exercise real compression work.
	for _, concurrency := range []int{1, 2, 4, 8, 16} {
		b.Run(fmt.Sprintf("concurrency_%d", concurrency), func(b *testing.B) {
			root := b.TempDir()
			binary := filepath.Join(root, "service")
			if err := os.WriteFile(binary, syntheticPayload(size), 0o755); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			var next atomic.Int64
			errs := make(chan error, concurrency)
			b.ResetTimer()
			var workers sync.WaitGroup
			for worker := 0; worker < concurrency; worker++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					for {
						n := next.Add(1) - 1
						if n >= int64(b.N) {
							return
						}
						output := filepath.Join(root, fmt.Sprintf("layout-%d", n))
						if _, err := Build(Options{
							Binary:       binary,
							Output:       output,
							Architecture: "amd64",
							Created:      time.Unix(0, 0),
						}); err != nil {
							errs <- err
							return
						}
					}
				}()
			}
			workers.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkNaiveTarGzip is a reference point, not a competitor to beat by
// magic: it's the simplest possible "tar one file, gzip it" using only
// archive/tar and compress/gzip at the default compression level, with
// none of Build()'s determinism (fixed timestamps, sorted directory
// creation, USTAR format), validation, OCI config/manifest/index, or
// atomic install. Comparing it to BenchmarkMakeLayer at the same payload
// size shows what OCI-layout correctness and determinism actually cost
// over the bare minimum - not a marketing number against a different tool
// doing a different job (see the wiki's Benchmarks page for why this
// project doesn't claim head-to-head parity with umoci/BuildKit/crane).
func BenchmarkNaiveTarGzip(b *testing.B) {
	for _, size := range payloadSizes {
		b.Run(fmt.Sprintf("payload_%dKiB", size>>10), func(b *testing.B) {
			data := syntheticPayload(size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var out bytes.Buffer
				gz := gzip.NewWriter(&out)
				tw := tar.NewWriter(gz)
				if err := tw.WriteHeader(&tar.Header{Name: "service", Mode: 0555, Size: int64(len(data))}); err != nil {
					b.Fatal(err)
				}
				if _, err := tw.Write(data); err != nil {
					b.Fatal(err)
				}
				if err := tw.Close(); err != nil {
					b.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
