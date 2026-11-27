// garmin-clean deletes duplicate activities from Garmin Connect.
//
// It lists the account's activities, groups them by start time, and for
// every group of two or more (a duplicate pair created by re-uploading,
// e.g. after a fresh database reset) deletes every member except the
// oldest (lowest activity id) — the original copy. Activities with a
// unique start time are never touched.
//
// The database is only used for Garmin credentials.
//
// Dry run by default; review the output, then add -delete.
//
// Usage:
//
//	go run ./cmd/garmin-clean -db 3rdparty2garmin.db            # dry run
//	go run ./cmd/garmin-clean -db 3rdparty2garmin.db -delete    # delete
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/tommartensen/3rdparty2garmin/internal/db"
	"github.com/tommartensen/3rdparty2garmin/internal/fit"
	"github.com/tommartensen/3rdparty2garmin/internal/garmin"
)

// matchTolerance is how far apart two activities' start times may be to
// count as copies of the same workout.
const matchTolerance = 2 * time.Minute

// parseGMT parses Garmin's startTimeGMT ("2021-06-03 09:23:41.0").
func parseGMT(s string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.0",
		"2006-01-02 15:04:05.00",
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
		time.RFC3339,
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q", s)
}

func main() {
	dbPath := flag.String("db", "3rdparty2garmin.db", "app database (Garmin credentials)")
	doDelete := flag.Bool("delete", false, "delete matched activities (default: dry run)")
	flag.Parse()

	if err := run(*dbPath, *doDelete); err != nil {
		log.Fatal(err)
	}
}

func run(dbPath string, doDelete bool) error {
	store, err := db.New(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = store.Close() }()

	userIDs, err := store.GarminUserIDs()
	if err != nil {
		return fmt.Errorf("find garmin users: %w", err)
	}
	if len(userIDs) != 1 {
		return fmt.Errorf("expected exactly one garmin-connected user, found %d", len(userIDs))
	}
	userID := userIDs[0]

	tokens, err := store.GetGarminTokens(userID)
	if err != nil {
		return fmt.Errorf("garmin tokens: %w", err)
	}
	o1 := &garmin.OAuth1Token{Token: tokens.OAuth1Token, TokenSecret: tokens.OAuth1TokenSecret}
	o2 := &garmin.OAuth2Token{AccessToken: tokens.OAuth2AccessToken, RefreshToken: tokens.OAuth2RefreshToken, ExpiresAt: tokens.OAuth2Expiry}
	if o2.Expired() {
		o2, err = garmin.RefreshOAuth2(o1)
		if err != nil {
			return fmt.Errorf("refresh garmin token: %w", err)
		}
		if err := store.UpdateGarminOAuth2(userID, o2.AccessToken, o2.RefreshToken, o2.ExpiresAt); err != nil {
			log.Printf("update garmin tokens: %v", err)
		}
	}

	activities, err := garmin.ListActivities(o2)
	if err != nil {
		return fmt.Errorf("list activities: %w", err)
	}

	type actTime struct {
		act   garmin.Activity
		start time.Time
	}
	oursSerial := json.Number(strconv.FormatUint(uint64(fit.FakeSerialNumber), 10))
	var acts []actTime
	seen := make(map[int64]bool)
	for _, a := range activities {
		if seen[a.ID] {
			continue // pagination can deliver the same activity twice
		}
		seen[a.ID] = true
		t, err := parseGMT(a.StartTimeGMT)
		if err != nil {
			log.Printf("skip activity %d: %v", a.ID, err)
			continue
		}
		acts = append(acts, actTime{act: a, start: t})
	}
	sort.Slice(acts, func(i, j int) bool { return acts[i].start.Before(acts[j].start) })
	log.Printf("%d activities on Garmin", len(acts))

	// Group activities whose start times fall within matchTolerance. Only
	// copies uploaded by this app (our spoofed serial) are deleted; groups
	// of foreign activities are reported and left alone.
	var toDelete []garmin.Activity
	groups := 0
	for i := 0; i < len(acts); {
		j := i + 1
		for j < len(acts) && acts[j].start.Sub(acts[i].start) <= matchTolerance {
			j++
		}
		if group := acts[i:j]; len(group) >= 2 {
			groups++
			sort.Slice(group, func(a, b int) bool { return group[a].act.ID < group[b].act.ID })
			log.Printf("  group %s:", group[0].start.Format("2006-01-02 15:04"))
			ours := 0
			for _, at := range group {
				if at.act.DeviceID == oursSerial {
					ours++
				}
			}
			if ours == 0 {
				log.Printf("    pre-existing duplicate (no app upload) — left alone")
				for _, at := range group {
					log.Printf("    %d  %q  device %v", at.act.ID, at.act.Name, at.act.DeviceID)
				}
				i = j
				continue
			}
			// If every copy is ours, keep the oldest; otherwise delete only ours.
			allOurs := ours == len(group)
			for k, at := range group {
				isOurs := at.act.DeviceID == oursSerial
				if !isOurs || (allOurs && k == 0) {
					log.Printf("    %d  %q  (keep, device %v)", at.act.ID, at.act.Name, at.act.DeviceID)
					continue
				}
				log.Printf("    %d  %q  DELETE https://connect.garmin.com/app/activity/%d (device %v)", at.act.ID, at.act.Name, at.act.ID, at.act.DeviceID)
				toDelete = append(toDelete, at.act)
			}
		}
		i = j
	}

	if len(toDelete) == 0 {
		log.Printf("no duplicate groups found — nothing to delete")
		return nil
	}
	log.Printf("%d duplicate groups, %d activities to delete", groups, len(toDelete))
	if !doDelete {
		log.Printf("dry run — re-run with -delete to delete them")
		return nil
	}
	for i, a := range toDelete {
		if err := garmin.DeleteActivity(o2, a.ID); err != nil {
			log.Printf("delete %d failed: %v", a.ID, err)
			continue
		}
		log.Printf("deleted %d/%d: %d", i+1, len(toDelete), a.ID)
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}
