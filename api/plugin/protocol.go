// Package plugin preserves the original plugin SDK import path.
//
// Deprecated: use github.com/CYPT71/secure-oci-base/sdk/plugin.
package plugin

import (
	"bufio"
	"encoding/json"
	"io"

	sdk "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

const (
	ContentType     = sdk.ContentType
	ProtocolVersion = sdk.ProtocolVersion
)

type Request = sdk.Request
type Response = sdk.Response
type RPCError = sdk.RPCError

func WriteMessage(w io.Writer, value any) error            { return sdk.WriteMessage(w, value) }
func ReadMessage(r *bufio.Reader) (json.RawMessage, error) { return sdk.ReadMessage(r) }
