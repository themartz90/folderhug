package appcli

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/themartz90/folderhug/internal/buildinfo"
)

func TestVersionDoesNotReadRuntimeConfiguration(t *testing.T) {
	t.Setenv("FOLDERHUG_PUBLIC_URL", "invalid")
	originalArgs, originalOutput := os.Args, os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer func() { os.Args, os.Stdout = originalArgs, originalOutput }()
	os.Args, os.Stdout = []string{"folderhug", "version"}, writer
	err = run()
	writer.Close()
	data, readErr := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("version read runtime configuration: %v", err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	var info buildinfo.Info
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info != buildinfo.Current() {
		t.Fatalf("version = %+v, want %+v", info, buildinfo.Current())
	}
}
