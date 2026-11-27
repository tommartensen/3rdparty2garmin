// Package fit post-processes source activity FIT files for Garmin Connect.
// Garmin ignores first-party analytics (Load, Readiness, VO2max, ...) for
// non-Garmin manufacturers, so device identity is rewritten before upload.
package fit

import (
	"bytes"
	"fmt"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

// FIT protocol invalid sentinel values per base type.
const (
	uint16Invalid = uint16(0xFFFF)
	uint8Invalid  = uint8(0xFF)
	sint8Invalid  = int8(0x7F)
)

// Device spoofing constants.
const (
	garminManufacturer = typedef.ManufacturerGarmin   // 1
	fenix6sProduct     = typedef.GarminProductFenix6s // 3288
	// FakeSerialNumber is the serial written into uploaded files; activities
	// carrying it were uploaded by this app's current pipeline.
	FakeSerialNumber = uint32(3420897194)
)

// Options controls source-specific FIT processing.
type Options struct {
	// StripTemperature removes bogus temperature records (MyWhoosh virtual rides).
	StripTemperature bool
}

// Fix backfills missing session averages from records, spoofs the device
// identity as a Garmin Fenix 6S, and returns the re-encoded file.
func Fix(data []byte, opts Options) ([]byte, error) {
	lis := filedef.NewListener()
	defer lis.Close()

	dec := decoder.New(bytes.NewReader(data),
		decoder.WithMesgListener(lis),
		decoder.WithBroadcastOnly(),
	)
	if _, err := dec.Decode(); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	activity, ok := lis.File().(*filedef.Activity)
	if !ok {
		return nil, fmt.Errorf("not an activity file (got %T)", lis.File())
	}

	// Collect metrics from records for the session average backfill.
	var powers []uint16
	var heartRates, cadences []uint8
	for _, rec := range activity.Records {
		if opts.StripTemperature {
			rec.Temperature = sint8Invalid
		}
		if rec.Power != uint16Invalid {
			powers = append(powers, rec.Power)
		}
		if rec.HeartRate != uint8Invalid {
			heartRates = append(heartRates, rec.HeartRate)
		}
		if rec.Cadence != uint8Invalid {
			cadences = append(cadences, rec.Cadence)
		}
	}

	for _, sess := range activity.Sessions {
		if shouldFixU16(sess.AvgPower) && len(powers) > 0 {
			sess.AvgPower = avgU16(powers)
		}
		if shouldFixU8(sess.AvgHeartRate) && len(heartRates) > 0 {
			sess.AvgHeartRate = avgU8(heartRates)
		}
		if shouldFixU8(sess.AvgCadence) && len(cadences) > 0 {
			sess.AvgCadence = avgU8(cadences)
		}
	}

	spoofDevice(activity)

	fit := activity.ToFIT(nil)

	var buf bytes.Buffer
	if err := encoder.New(&buf, encoder.WithProtocolVersion(proto.V2)).Encode(&fit); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return buf.Bytes(), nil
}

func shouldFixU16(v uint16) bool { return v == uint16Invalid || v == 0 }
func shouldFixU8(v uint8) bool   { return v == uint8Invalid || v == 0 }

func avgU16(vals []uint16) uint16 {
	var sum uint64
	for _, v := range vals {
		sum += uint64(v)
	}
	return uint16(sum / uint64(len(vals)))
}

func avgU8(vals []uint8) uint8 {
	var sum uint64
	for _, v := range vals {
		sum += uint64(v)
	}
	return uint8(sum / uint64(len(vals)))
}

func spoofDevice(activity *filedef.Activity) {
	activity.FileId.Manufacturer = garminManufacturer
	activity.FileId.Product = fenix6sProduct.Uint16()
	activity.FileId.SerialNumber = FakeSerialNumber

	for _, di := range activity.DeviceInfos {
		di.Manufacturer = garminManufacturer
		di.Product = fenix6sProduct.Uint16()
		di.SerialNumber = FakeSerialNumber
	}
}
