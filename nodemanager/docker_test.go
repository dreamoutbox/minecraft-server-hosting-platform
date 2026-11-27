package nodemanager

import (
	"bytes"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

func TestDemuxLogStreamPreservesInterleavedOrder(t *testing.T) {
	var raw bytes.Buffer
	stdoutWriter := stdcopy.NewStdWriter(&raw, stdcopy.Stdout)
	stderrWriter := stdcopy.NewStdWriter(&raw, stdcopy.Stderr)

	frames := []struct {
		writer func([]byte) (int, error)
		line   string
	}{
		{stdoutWriter.Write, "[init] Starting server...\n"},
		{stderrWriter.Write, "WARNING: A restricted method in java.lang.System has been called\n"},
		{stdoutWriter.Write, "[Server thread/INFO]: Starting minecraft server version 1.21\n"},
		{stderrWriter.Write, "WARNING: A terminally deprecated method in sun.misc.Unsafe has been called\n"},
		{stdoutWriter.Write, "[Server thread/INFO]: Done (1.234s)!\n"},
	}

	expected := ""
	for _, frame := range frames {
		if _, err := frame.writer([]byte(frame.line)); err != nil {
			t.Fatalf("failed writing frame: %v", err)
		}
		expected += frame.line
	}

	got, err := demuxLogStream(&raw)
	if err != nil {
		t.Fatalf("demuxLogStream returned error: %v", err)
	}
	if got != expected {
		t.Fatalf("interleaved log order mismatch:\ngot:\n%s\nwant:\n%s", got, expected)
	}
}
