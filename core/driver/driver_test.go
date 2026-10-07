// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	driverv1 "github.com/portenv/portenv/proto/gen/go/portenv/driver/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

func methodNames(t reflect.Type) []string {
	var names []string
	for i := range t.NumMethod() {
		if name := t.Method(i).Name; !strings.HasPrefix(name, "mustEmbed") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func TestInterfaceMatchesProto(t *testing.T) {
	got := methodNames(reflect.TypeFor[Driver]())
	want := methodNames(reflect.TypeFor[driverv1.DriverServiceServer]())
	if !slices.Equal(got, want) {
		t.Fatalf("Driver methods %v, DriverService RPCs %v: keep them in step", got, want)
	}
}

func TestEnumsMatchProto(t *testing.T) {
	cases := []struct {
		name string
		goN  int
		pbN  int
	}{
		{"State", int(StateFailed) + 1, len(typesv1.BoxState_name)},
		{"Architecture", int(ArchitectureAMD64) + 1, len(typesv1.Architecture_name)},
		{"Isolation", int(IsolationVM) + 1, len(driverv1.Isolation_name)},
		{"LogStream", int(LogStreamStderr) + 1, len(driverv1.LogStream_name)},
	}
	for _, c := range cases {
		if c.goN != c.pbN {
			t.Errorf("%s has %d values in Go and %d in proto", c.name, c.goN, c.pbN)
		}
	}
}
