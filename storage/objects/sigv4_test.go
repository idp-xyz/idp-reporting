package objects

import (
	"testing"
	"time"
)

// The fixtures are the worked examples of the AWS Signature Version 4 documentation ("Signature Calculations for the Authorization Header:
// Transferring Payload in a Single Chunk": GET Object, PUT Object, GET Bucket (List Objects) and GET Bucket lifecycle): the credentials, the
// request, the canonical request, the string to sign and the signature are the published ones, written here as literals and not computed.
// Every example is of the bucket examplebucket in the virtual-hosted form (Host: examplebucket.s3.amazonaws.com), at 2013-05-24T00:00:00Z in
// us-east-1, with the published example credentials.
var example = credentials{access: "AKIAIOSFODNN7EXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", region: "us-east-1"}

var exampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

const exampleHost = "examplebucket.s3.amazonaws.com"

func TestSigV4MatchesTheSpecificationsPublishedExamples(t *testing.T) {
	cases := []struct {
		name        string
		method      string
		path        string
		query       [][2]string
		headers     map[string]string
		payloadHash string
		canonical   string
		signed      string
		toSign      string
		signature   string
	}{
		{
			name:   "GET Object",
			method: "GET",
			path:   "/test.txt",
			headers: map[string]string{
				"Range":                "bytes=0-9",
				"x-amz-content-sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				"x-amz-date":           "20130524T000000Z",
			},
			payloadHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			canonical: "GET\n/test.txt\n\nhost:examplebucket.s3.amazonaws.com\nrange:bytes=0-9\n" +
				"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\nx-amz-date:20130524T000000Z\n\n" +
				"host;range;x-amz-content-sha256;x-amz-date\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			signed:    "host;range;x-amz-content-sha256;x-amz-date",
			toSign:    "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972",
			signature: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			// The key has a "$" in it: the canonical URI is /test%24file.text, encoded once.
			name:   "PUT Object",
			method: "PUT",
			path:   "/test$file.text",
			headers: map[string]string{
				"Date":                 "Fri, 24 May 2013 00:00:00 GMT",
				"x-amz-date":           "20130524T000000Z",
				"x-amz-storage-class":  "REDUCED_REDUNDANCY",
				"x-amz-content-sha256": "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			},
			payloadHash: "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			canonical: "PUT\n/test%24file.text\n\ndate:Fri, 24 May 2013 00:00:00 GMT\nhost:examplebucket.s3.amazonaws.com\n" +
				"x-amz-content-sha256:44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072\nx-amz-date:20130524T000000Z\nx-amz-storage-class:REDUCED_REDUNDANCY\n\n" +
				"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class\n44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			signed:    "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			toSign:    "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n9e0e90d9c76de8fa5b200d8c849cd5b8dc7a3be3951ddb7f6a76b4158342019d",
			signature: "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			// The query is sorted by name: prefix=J&max-keys=2 is signed as max-keys=2&prefix=J.
			name:   "GET Bucket (List Objects)",
			method: "GET",
			path:   "/",
			query:  [][2]string{{"prefix", "J"}, {"max-keys", "2"}},
			headers: map[string]string{
				"x-amz-content-sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				"x-amz-date":           "20130524T000000Z",
			},
			payloadHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			canonical: "GET\n/\nmax-keys=2&prefix=J\nhost:examplebucket.s3.amazonaws.com\n" +
				"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\nx-amz-date:20130524T000000Z\n\n" +
				"host;x-amz-content-sha256;x-amz-date\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			signed:    "host;x-amz-content-sha256;x-amz-date",
			toSign:    "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\ndf57d21db20da04d7fa30298dd4488ba3a2b47ca3a489c74750e0f1e7df1b9b7",
			signature: "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
		{
			// A subresource without a value is signed as lifecycle= (name, equals sign, empty value).
			name:   "GET Bucket lifecycle",
			method: "GET",
			path:   "/",
			query:  [][2]string{{"lifecycle", ""}},
			headers: map[string]string{
				"x-amz-content-sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				"x-amz-date":           "20130524T000000Z",
			},
			payloadHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			canonical: "GET\n/\nlifecycle=\nhost:examplebucket.s3.amazonaws.com\n" +
				"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\nx-amz-date:20130524T000000Z\n\n" +
				"host;x-amz-content-sha256;x-amz-date\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			signed:    "host;x-amz-content-sha256;x-amz-date",
			toSign:    "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n9766c798316ff2757b517bc739a67f6213b4ab36dd5da2f94eaebf79c77395ca",
			signature: "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := derive(example, c.method, exampleHost, c.path, c.query, c.headers, c.payloadHash, exampleTime)
			if d.CanonicalRequest != c.canonical {
				t.Errorf("canonical request:\n%q\nwant\n%q", d.CanonicalRequest, c.canonical)
			}
			if d.SignedHeaders != c.signed {
				t.Errorf("signed headers %q, want %q", d.SignedHeaders, c.signed)
			}
			if d.StringToSign != c.toSign {
				t.Errorf("string to sign:\n%q\nwant\n%q", d.StringToSign, c.toSign)
			}
			if d.Signature != c.signature {
				t.Errorf("signature %s, want %s", d.Signature, c.signature)
			}
			want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=" + c.signed + ", Signature=" + c.signature
			if got := authorization(example, d, exampleTime); got != want {
				t.Errorf("authorization %q, want %q", got, want)
			}
		})
	}
}
