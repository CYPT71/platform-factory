package conformance

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"reflect"
	"testing"

	apiplugin "github.com/CYPT71/secure-oci-base/api/plugin"
	hostplugin "github.com/CYPT71/secure-oci-base/internal/plugin"
	sdkplugin "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

func TestPluginWireV1SDKAndHostRemainBidirectionallyCompatible(t *testing.T) {
	request := hostplugin.Request{ID: "17", Method: "v1.deployment.apply", Params: json.RawMessage(`{"desired":"ready"}`), TraceID: "trace-1", OperationID: "operation-1"}
	var wire bytes.Buffer
	if err := hostplugin.WriteMessage(&wire, request); err != nil {
		t.Fatal(err)
	}
	raw, err := sdkplugin.ReadMessage(bufio.NewReader(&wire))
	if err != nil {
		t.Fatal(err)
	}
	var sdkRequest sdkplugin.Request
	if err := json.Unmarshal(raw, &sdkRequest); err != nil {
		t.Fatal(err)
	}
	if sdkRequest.ID != request.ID || sdkRequest.Method != request.Method || sdkRequest.TraceID != request.TraceID || sdkRequest.OperationID != request.OperationID || !bytes.Equal(sdkRequest.Params, request.Params) {
		t.Fatalf("SDK decoded host request incorrectly: %+v", sdkRequest)
	}

	wire.Reset()
	sdkResponse := sdkplugin.Response{ID: request.ID, Result: json.RawMessage(`{"state":"ready"}`), TraceID: request.TraceID, OperationID: request.OperationID}
	if err := sdkplugin.WriteMessage(&wire, sdkResponse); err != nil {
		t.Fatal(err)
	}
	raw, err = hostplugin.ReadMessage(bufio.NewReader(&wire))
	if err != nil {
		t.Fatal(err)
	}
	var hostResponse hostplugin.Response
	if err := json.Unmarshal(raw, &hostResponse); err != nil {
		t.Fatal(err)
	}
	if hostResponse.ID != sdkResponse.ID || hostResponse.TraceID != sdkResponse.TraceID || hostResponse.OperationID != sdkResponse.OperationID || !bytes.Equal(hostResponse.Result, sdkResponse.Result) {
		t.Fatalf("host decoded SDK response incorrectly: %+v", hostResponse)
	}
}

func TestPublicManifestToHostConversionPreservesButCannotIncreaseAuthority(t *testing.T) {
	public := apiplugin.Manifest{
		APIVersion: apiplugin.ManifestAPIVersion,
		Name:       "adapter", Version: "v1", Family: apiplugin.PluginFamilyDeployment,
		Capabilities: []string{"deployment.apply"},
		Permissions:  apiplugin.PluginPermissions{Network: []string{"registry.example"}, Filesystem: []string{"workspace"}, Secrets: []string{"registry-token"}},
		Platforms:    []string{"linux/amd64"}, Executable: "adapter", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := public.Sign(privateKey, "release-key"); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	var host hostplugin.Manifest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&host); err != nil {
		t.Fatal(err)
	}
	if err := host.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := host.VerifySignature([]ed25519.PublicKey{publicKey}); err != nil {
		t.Fatalf("host rejected public manifest signature: %v", err)
	}
	publicSigningBytes, err := public.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	hostSigningBytes, err := host.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicSigningBytes, hostSigningBytes) {
		t.Fatalf("host and public manifest signing bytes differ:\npublic=%s\nhost=%s", publicSigningBytes, hostSigningBytes)
	}
	if string(host.Family) != string(public.Family) || !reflect.DeepEqual(host.Capabilities, public.Capabilities) || !reflect.DeepEqual(host.Permissions.Network, public.Permissions.Network) || !reflect.DeepEqual(host.Permissions.Filesystem, public.Permissions.Filesystem) || !reflect.DeepEqual(host.Permissions.Secrets, public.Permissions.Secrets) {
		t.Fatalf("conversion changed requested authority: public=%+v host=%+v", public, host)
	}
}
