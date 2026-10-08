package release

import (
	"fmt"
	"reflect"
	"testing"
)

// requireShim keeps these tests identical to MoonLightRetail's copies
// without adding a test dependency to MoonLightCloud.
type requireShim struct{}

var require requireShim

func describe(msg []any) string {
	if len(msg) == 0 {
		return ""
	}
	if format, ok := msg[0].(string); ok {
		return fmt.Sprintf(format, msg[1:]...)
	}
	return fmt.Sprint(msg...)
}

func (requireShim) NoError(t *testing.T, err error, msg ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v %s", err, describe(msg))
	}
}

func (requireShim) Error(t *testing.T, err error, msg ...any) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error %s", describe(msg))
	}
}

func (requireShim) Equal(t *testing.T, want, got any, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("want %#v, got %#v %s", want, got, describe(msg))
	}
}
