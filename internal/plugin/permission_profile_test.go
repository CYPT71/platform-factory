package plugin

import "testing"

func TestPermissionProfileForKnownFamilyReturnsItsOwnProfile(t *testing.T) {
	got := permissionProfileFor(PluginFamilyBuild)
	want := permissionProfiles[PluginFamilyBuild]
	if got != want {
		t.Fatalf("got=%+v, want=%+v", got, want)
	}
}

func TestPermissionProfileForUnknownFamilyFallsBackToDefault(t *testing.T) {
	for _, family := range []PluginFamily{"", "not-a-real-family"} {
		if got := permissionProfileFor(family); got != defaultPermissionProfile {
			t.Fatalf("family=%q got=%+v, want default %+v", family, got, defaultPermissionProfile)
		}
	}
}
