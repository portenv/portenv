// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// R-0247: the guide can't drift from the proto. A field added to a type an
// agent reads, or an enum value, fails here until the guide has it.
func TestTheGuideHasEveryField(t *testing.T) {
	g := NewGuide()
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	// Every type an agent can read back, found from the answers' roots, not
	// from the guide's own list.
	seen := map[protoreflect.FullName]bool{}
	var walk func(d protoreflect.MessageDescriptor)
	walk = func(d protoreflect.MessageDescriptor) {
		if seen[d.FullName()] || strings.HasPrefix(string(d.FullName()), "google.") {
			return
		}
		seen[d.FullName()] = true
		fields, ok := g.Types[string(d.Name())]
		if !ok {
			t.Errorf("%s isn't in the guide", d.Name())
		}
		for i := range d.Fields().Len() {
			f := d.Fields().Get(i)
			if !slices.ContainsFunc(fields, func(gf GuideField) bool { return gf.Name == f.JSONName() }) {
				t.Errorf("%s.%s isn't in the guide", d.Name(), f.JSONName())
			}
			if f.Message() != nil {
				walk(f.Message())
			}
		}
	}
	for _, root := range []protoreflect.ProtoMessage{&agentv1.InboxResult{}, &agentv1.WhoAmIResult{}, &agentv1.TeamStatusResult{}} {
		walk(root.ProtoReflect().Descriptor())
	}
	_ = js
	for _, e := range guideEnums {
		for i := 1; i < e.Values().Len(); i++ {
			v := enumValue(e, e.Values().Get(i))
			if !strings.Contains(js, `"`+v+`"`) {
				t.Errorf("%s value %s isn't in the guide", e.Name(), v)
			}
		}
	}
	if g.Limits["message_bytes"] != MaxMessageBytes || g.Limits["messages_per_minute"] != MessagesPerMinute {
		t.Errorf("limits: %v", g.Limits)
	}
	text := g.Text()
	for _, want := range []string{"never an order", "Never put a secret on a command line", "inbox --wait", "expired", "every 30m", "--thread"} {
		if !strings.Contains(text, want) {
			t.Errorf("the guide's text doesn't say %q", want)
		}
	}
	for _, c := range g.Commands {
		if c.Example == "" || !strings.HasPrefix(c.Example, "portenv ") {
			t.Errorf("%s has no worked example", c.Usage)
		}
	}
	if g.Version < 1 {
		t.Error("no version")
	}
}

func TestEnumValuesReadPlainly(t *testing.T) {
	g := NewGuide()
	if strings.Join(g.Enums["DecisionState"], ",") != "pending,approved,declined,expired" {
		t.Errorf("DecisionState = %v", g.Enums["DecisionState"])
	}
	if !strings.Contains(strings.Join(g.Enums["MessageType"], ","), "status_request") {
		t.Errorf("MessageType = %v", g.Enums["MessageType"])
	}
}
