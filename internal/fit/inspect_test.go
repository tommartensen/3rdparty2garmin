package fit

import (
	"bytes"
	"os"
	"testing"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
)

// TestInspectFixedFile runs a real FIT file through Fix and dumps the
// device identity of the output. Enable with FIT_INSPECT=<path>.
func TestInspectFixedFile(t *testing.T) {
	path := os.Getenv("FIT_INSPECT")
	if path == "" {
		t.Skip("set FIT_INSPECT to a FIT file path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := Fix(raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("input %d bytes -> fixed %d bytes", len(raw), len(fixed))

	lis := filedef.NewListener()
	defer lis.Close()
	dec := decoder.New(bytes.NewReader(fixed),
		decoder.WithMesgListener(lis),
		decoder.WithBroadcastOnly(),
	)
	if _, err := dec.Decode(); err != nil {
		t.Fatal(err)
	}
	activity, ok := lis.File().(*filedef.Activity)
	if !ok {
		t.Fatalf("not an activity file: %T", lis.File())
	}
	t.Logf("file_id:    manufacturer=%d product=%d serial=%d",
		activity.FileId.Manufacturer, activity.FileId.Product, activity.FileId.SerialNumber)
	for i, di := range activity.DeviceInfos {
		t.Logf("device_info[%d]: manufacturer=%d product=%d serial=%d", i, di.Manufacturer, di.Product, di.SerialNumber)
	}
	t.Logf("sessions=%d records=%d", len(activity.Sessions), len(activity.Records))
}
