package main_test

import (
	"context"
	"os"
	"testing"
	"time"

	"idp.local/reporting/storage/objects"
)

// The platform never creates a bucket (ADR 0007 decision 6) and a source test holds every non-test file to that, so the drill's bucket is made
// by this test, which scripts/restore_drill.sh runs with REPORTING_RESTORE_DRILL_CREATE_BUCKET=1 against the throwaway S3-compatible server. In
// every other run it does nothing: it is not a qualification and has no gate of its own.
func TestTheDrillsBucketIsMadeByATestAlone(t *testing.T) {
	if os.Getenv("REPORTING_RESTORE_DRILL_CREATE_BUCKET") != "1" {
		t.Log("not asked to create the drill's bucket")
		return
	}
	endpoint, access, secret := os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT"), os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"), os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
	if endpoint == "" || access == "" || secret == "" {
		t.Fatal("REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY and _SECRET_KEY are needed")
	}
	c, err := objects.NewS3(objects.S3{Endpoint: endpoint, Bucket: "reporting-restore-s04f", Region: "us-east-1", AccessKey: access, SecretKey: secret, LoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = objects.CreateBucketForTests(ctx, c); err != nil {
		t.Fatalf("the drill's bucket could not be created: %v", err)
	}
}
