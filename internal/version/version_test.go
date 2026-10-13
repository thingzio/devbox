package version

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	built := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	cases := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"ldflags win", "v0.1.0-3-gabc1234", built("v0.1.0"), true, "v0.1.0-3-gabc1234"},
		{"go install", "dev", built("v0.1.0"), true, "v0.1.0"},
		{"source tree", "dev", built("(devel)"), true, "dev"},
		{"no module version", "dev", built(""), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
	}
	for _, c := range cases {
		if got := resolve(c.stamped, c.info, c.ok); got != c.want {
			t.Errorf("%s: resolve = %q, want %q", c.name, got, c.want)
		}
	}
}
