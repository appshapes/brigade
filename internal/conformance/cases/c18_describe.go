package cases

import (
	"reflect"
	"slices"
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c18Describe: `describe.limits`, `.lease` and `.retention` carry every
// member of the protocol's Limits, Lease and Retention types (the raw key
// set against the struct tags, nested rate pairs included), each a
// number > 0; `capabilities` contains `message.receive` (4.4.1, 4.5.13,
// 4.7). The raw document comes from a describe on a scratch principal.
func c18Describe() conformance.Case {
	return conformance.Case{
		ID:    "C-18",
		Rule:  "4.5.13 describe limits and capabilities",
		Title: "limits, lease and retention carry every member as a positive number; capabilities include message.receive",
		Tags:  []string{conformance.TagCore},
		Run:   runC18,
	}
}

func runC18(t *conformance.T) {
	r := t.Exec(t.Scratch("c18"), nil, "describe")
	result := t.OKRaw(r)
	checkMembers(t, "limits", result["limits"], reflect.TypeFor[protocol.Limits]())
	checkMembers(t, "lease", result["lease"], reflect.TypeFor[protocol.Lease]())
	checkMembers(t, "retention", result["retention"], reflect.TypeFor[protocol.Retention]())
	caps, _ := result["capabilities"].([]any)
	if !slices.Contains(caps, any("message.receive")) {
		t.Errorf("describe: capabilities lacks message.receive (4.7)")
	}
	if !slices.Contains(t.Describe().Capabilities, "message.receive") {
		t.Errorf("start-of-run describe: capabilities lacks message.receive (4.7)")
	}
}

// checkMembers walks the struct type's json tags: every tagged member
// must be present in the raw object, a nested struct is checked
// recursively, and every leaf is a number greater than zero.
func checkMembers(t *conformance.T, path string, raw any, typ reflect.Type) {
	obj, ok := raw.(map[string]any)
	if !ok {
		t.Errorf("describe: %s is absent or not an object (4.4.1)", path)
		return
	}
	for i := range typ.NumField() {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		member := path + "." + name
		value, present := obj[name]
		if !present {
			t.Errorf("describe: %s is absent (4.4.1, C-18)", member)
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			checkMembers(t, member, value, field.Type)
			continue
		}
		if n, isNum := value.(float64); !isNum || n <= 0 {
			t.Errorf("describe: %s is %v, want a number > 0 (4.4.1)", member, value)
		}
	}
}
