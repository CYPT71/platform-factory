package strictjson

import "testing"

func TestDecodeAcceptsOneKnownValueOnly(t *testing.T) {
	type record struct {
		Name string `json:"name"`
	}
	var value record
	if err := Decode([]byte(`{"name":"ok"}`), &value); err != nil || value.Name != "ok" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	for _, invalid := range []string{
		`{"name":"ok","unknown":true}`,
		`{"name":"ok"} {"name":"second"}`,
	} {
		if err := Decode([]byte(invalid), &value); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}
