package fit

import (
	"bytes"
	"testing"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

// encodeActivity builds FIT bytes from a filedef.Activity, mirroring the
// encode step in Fix.
func encodeActivity(t *testing.T, act *filedef.Activity) []byte {
	t.Helper()
	fit := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf, encoder.WithProtocolVersion(proto.V2)).Encode(&fit); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// decodeActivity parses FIT bytes back into a filedef.Activity.
func decodeActivity(t *testing.T, data []byte) *filedef.Activity {
	t.Helper()
	lis := filedef.NewListener()
	defer lis.Close()
	dec := decoder.New(bytes.NewReader(data),
		decoder.WithMesgListener(lis),
		decoder.WithBroadcastOnly(),
	)
	if _, err := dec.Decode(); err != nil {
		t.Fatalf("decode: %v", err)
	}
	act, ok := lis.File().(*filedef.Activity)
	if !ok {
		t.Fatalf("not an activity file (got %T)", lis.File())
	}
	return act
}

func TestFixSpoofsDeviceAndBackfillsAverages(t *testing.T) {
	ts := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	act := &filedef.Activity{
		FileId: mesgdef.FileId{
			Type:         typedef.FileActivity,
			TimeCreated:  ts,
			Manufacturer: 32, // Wahoo Fitness
			Product:      43, // ELEMNT
			SerialNumber: 262127974,
		},
		Sessions: []*mesgdef.Session{{
			Timestamp:        ts.Add(time.Hour),
			StartTime:        ts,
			TotalElapsedTime: 3600000,
			Sport:            typedef.SportCycling,
			// Averages left at zero — Fix should backfill them.
		}},
		Records: []*mesgdef.Record{
			{Timestamp: ts, Power: 200, HeartRate: 150, Cadence: 90},
			{Timestamp: ts.Add(time.Second), Power: 220, HeartRate: 155, Cadence: 92},
		},
	}

	fixed, err := Fix(encodeActivity(t, act))
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}

	out := decodeActivity(t, fixed)

	if out.FileId.Manufacturer != typedef.ManufacturerGarmin {
		t.Errorf("manufacturer = %d, want Garmin (%d)", out.FileId.Manufacturer, typedef.ManufacturerGarmin)
	}
	if want := typedef.GarminProductFenix6s.Uint16(); out.FileId.Product != want {
		t.Errorf("product = %d, want %d (Fenix 6S)", out.FileId.Product, want)
	}
	if len(out.DeviceInfos) > 0 && out.DeviceInfos[0].Manufacturer != typedef.ManufacturerGarmin {
		t.Errorf("device_info manufacturer = %d, want Garmin", out.DeviceInfos[0].Manufacturer)
	}

	if len(out.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(out.Sessions))
	}
	sess := out.Sessions[0]
	if sess.AvgPower != 210 {
		t.Errorf("avg power = %d, want 210", sess.AvgPower)
	}
	if sess.AvgHeartRate != 152 {
		t.Errorf("avg heart rate = %d, want 152", sess.AvgHeartRate)
	}
	if sess.AvgCadence != 91 {
		t.Errorf("avg cadence = %d, want 91", sess.AvgCadence)
	}
}

func TestFixKeepsValidAverages(t *testing.T) {
	ts := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	act := &filedef.Activity{
		FileId: mesgdef.FileId{
			Type:         typedef.FileActivity,
			TimeCreated:  ts,
			Manufacturer: 32,
			Product:      43,
			SerialNumber: 262127974,
		},
		Sessions: []*mesgdef.Session{{
			Timestamp:        ts.Add(time.Hour),
			StartTime:        ts,
			TotalElapsedTime: 3600000,
			Sport:            typedef.SportCycling,
			AvgPower:         250, // Device-computed — must survive untouched.
		}},
		Records: []*mesgdef.Record{
			{Timestamp: ts, Power: 200},
			{Timestamp: ts.Add(time.Second), Power: 220},
		},
	}

	fixed, err := Fix(encodeActivity(t, act))
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}

	sess := decodeActivity(t, fixed).Sessions[0]
	if sess.AvgPower != 250 {
		t.Errorf("avg power = %d, want 250 (valid average must be kept)", sess.AvgPower)
	}
}
