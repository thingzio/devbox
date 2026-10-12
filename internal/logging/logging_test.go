package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "json", true)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hello", "k", "v")
	if !strings.Contains(buf.String(), `"msg":"hello"`) || !strings.Contains(buf.String(), `"level":"DEBUG"`) {
		t.Fatalf("json debug output = %q", buf.String())
	}
	buf.Reset()
	log, _ = New(&buf, "text", false)
	log.Debug("hidden")
	log.Info("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "msg=shown") {
		t.Fatalf("text info output = %q", buf.String())
	}
	buf.Reset()
	log, _ = New(&buf, "auto", false)
	log.Info("auto")
	if !strings.HasPrefix(buf.String(), "{") {
		t.Fatalf("auto on a non-terminal should be JSON: %q", buf.String())
	}
	if _, err := New(&buf, "xml", false); err == nil {
		t.Fatal("unknown format accepted")
	}
}
