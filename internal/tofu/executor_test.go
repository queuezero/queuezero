package tofu

import "testing"

func TestParseOutputJSON_ScalarsAndLists(t *testing.T) {
	in := []byte(`{
	  "scripts_bucket": {"value": "gauss-q0-scripts", "type": "string"},
	  "controller_private_ip": {"value": "10.0.1.42", "type": "string"},
	  "subnet_ids": {"value": ["subnet-a", "subnet-b"], "type": ["list","string"]}
	}`)
	got, err := parseOutputJSON(in)
	if err != nil {
		t.Fatalf("parseOutputJSON: %v", err)
	}
	if got["scripts_bucket"] != "gauss-q0-scripts" {
		t.Errorf("scripts_bucket=%q want bare string", got["scripts_bucket"])
	}
	if got["controller_private_ip"] != "10.0.1.42" {
		t.Errorf("controller_private_ip=%q", got["controller_private_ip"])
	}
	// A list value is kept as its compact JSON encoding.
	if got["subnet_ids"] != `["subnet-a","subnet-b"]` && got["subnet_ids"] != `["subnet-a", "subnet-b"]` {
		t.Errorf("subnet_ids=%q want JSON array string", got["subnet_ids"])
	}
}

func TestParseOutputJSON_Empty(t *testing.T) {
	for _, in := range [][]byte{nil, []byte(``), []byte(`{}`)} {
		got, err := parseOutputJSON(in)
		if err != nil {
			t.Fatalf("parseOutputJSON(%q): %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("parseOutputJSON(%q) = %v, want empty", in, got)
		}
	}
}

func TestParseOutputJSON_Garbage(t *testing.T) {
	if _, err := parseOutputJSON([]byte(`not json`)); err == nil {
		t.Error("garbage input should error (the caller decides to treat it as empty)")
	}
}
