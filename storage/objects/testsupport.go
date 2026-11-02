package objects

import (
	"context"
	"encoding/xml"
	"net/http"

	r "idp.local/reporting/reporting"
)

// CreateBucketForTests creates the client's bucket with a PUT bucket request, for the qualification against a throwaway server whose bucket
// does not exist yet (docs/S04E_SCOPE.md E6). The platform never creates or configures buckets (ADR 0007 decision 6): nothing but a test
// calls this, and a source test holds it to that. A bucket that already exists is not an error.
func CreateBucketForTests(ctx context.Context, s *S3) error {
	var body []byte
	if s.Region != "us-east-1" {
		type configuration struct {
			XMLName            xml.Name `xml:"CreateBucketConfiguration"`
			LocationConstraint string   `xml:"LocationConstraint"`
		}
		body, _ = xml.Marshal(configuration{LocationConstraint: s.Region})
	}
	payload := emptySHA256
	if body != nil {
		payload = hashHex(string(body))
	}
	resp, err := s.do(ctx, http.MethodPut, "/"+s.Bucket, nil, body, payload)
	if err != nil {
		return err
	}
	defer resp.finish()
	// 200 created; 409 the bucket is already ours (BucketAlreadyOwnedByYou).
	if resp.status != http.StatusOK && resp.status != http.StatusConflict {
		return &r.ObjectStoreError{Status: resp.status}
	}
	return nil
}
