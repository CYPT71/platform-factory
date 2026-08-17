package provenance

import (
	"strings"
	"testing"
)

func FuzzMigrationArtifactMetadata(f *testing.F) {
	f.Add("sha256:"+strings.Repeat("a", 64), int64(10), "oci-layout.tar.gz", "plugin-a")
	f.Add("bad", int64(-1), "", "secret-sentinel")
	f.Fuzz(func(t *testing.T, digest string, size int64, format, pluginID string) {
		if len(digest) > 256 || len(format) > 256 || len(pluginID) > 256 {
			t.Skip()
		}
		record := MigrationArtifactRecord{FormatVersion: MigrationArtifactFormatVersion, TraceID: "fuzz", OperationID: "migration-fuzz", ResourceID: "resource", Digest: digest, Size: size, Format: format, SourcePluginID: pluginID, SourcePluginDigest: digest, TargetPluginID: "target", TargetPluginDigest: digest}
		_ = validateMigrationArtifactRecord(record)
	})
}
