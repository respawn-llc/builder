package serverapi

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestUpdateStatusResultJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		want UpdateStatusResult
		json string
	}{
		{
			name: "current",
			want: CurrentUpdateStatusResult("1.2.3", "1.2.3"),
			json: `{"kind":"current","current_version":"1.2.3","latest_version":"1.2.3"}`,
		},
		{
			name: "available",
			want: AvailableUpdateStatusResult("1.2.3", "1.3.0"),
			json: `{"kind":"available","current_version":"1.2.3","latest_version":"1.3.0"}`,
		},
		{
			name: "check unavailable",
			want: CheckUnavailableUpdateStatusResult(),
			json: `{"kind":"check_unavailable"}`,
		},
		{
			name: "check failed",
			want: FailedUpdateStatusResult("403 Forbidden"),
			json: `{"kind":"check_failed","cause":"403 Forbidden"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.want.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			encoded, err := json.Marshal(test.want)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(encoded) != test.json {
				t.Fatalf("JSON = %s, want %s", encoded, test.json)
			}

			var decoded UpdateStatusResult
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			roundTrip, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("Marshal decoded: %v", err)
			}
			if !bytes.Equal(roundTrip, encoded) {
				t.Fatalf("decoded JSON = %s, want %s", roundTrip, encoded)
			}
			assertUpdateStatusResultAccessors(t, decoded)
		})
	}
}

func TestUpdateStatusResultAccessorsAreVariantSpecific(t *testing.T) {
	current := CurrentUpdateStatusResult("1.2.3", "1.2.3")
	if current.Kind() != UpdateStatusCurrent {
		t.Fatalf("current kind = %q, want %q", current.Kind(), UpdateStatusCurrent)
	}
	currentVersion, latestVersion, ok := current.Versions()
	if !ok || currentVersion != "1.2.3" || latestVersion != "1.2.3" {
		t.Fatalf("current versions = %q/%q/%v", currentVersion, latestVersion, ok)
	}
	if _, ok := current.FailureCause(); ok {
		t.Fatal("current result exposed a failure cause")
	}

	failed := FailedUpdateStatusResult("invalid release metadata")
	if failed.Kind() != UpdateStatusCheckFailed {
		t.Fatalf("failed kind = %q, want %q", failed.Kind(), UpdateStatusCheckFailed)
	}
	if _, _, ok := failed.Versions(); ok {
		t.Fatal("failed result exposed versions")
	}
	cause, ok := failed.FailureCause()
	if !ok || cause != "invalid release metadata" {
		t.Fatalf("failure cause = %q/%v", cause, ok)
	}

	unavailable := CheckUnavailableUpdateStatusResult()
	if _, _, ok := unavailable.Versions(); ok {
		t.Fatal("check-unavailable result exposed versions")
	}
	if _, ok := unavailable.FailureCause(); ok {
		t.Fatal("check-unavailable result exposed a failure cause")
	}
}

func TestUpdateStatusResultRejectsInvalidJSON(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`null`,
		`{"kind":"unknown"}`,
		`{"kind":"current"}`,
		`{"kind":"current","current_version":"","latest_version":"1.2.3"}`,
		`{"kind":"current","current_version":"1.2.3","latest_version":""}`,
		`{"kind":"current","current_version":" 1.2.3","latest_version":"1.2.3"}`,
		`{"kind":"current","current_version":"1.2.3","latest_version":"1.2.3","cause":"failure"}`,
		`{"kind":"available"}`,
		`{"kind":"available","current_version":"1.2.3"}`,
		`{"kind":"available","latest_version":"1.3.0"}`,
		`{"kind":"available","current_version":"1.2.3","latest_version":"1.3.0","cause":null}`,
		`{"kind":"check_unavailable","current_version":null}`,
		`{"kind":"check_unavailable","latest_version":null}`,
		`{"kind":"check_unavailable","cause":null}`,
		`{"kind":"check_failed"}`,
		`{"kind":"check_failed","cause":""}`,
		`{"kind":"check_failed","cause":" \t "}`,
		`{"kind":"check_failed","cause":"failure","current_version":null}`,
		`{"kind":"check_failed","cause":"failure","latest_version":null}`,
		`{"kind":"check_failed","cause":"failure","unknown":true}`,
		`{"kind":"check_unavailable"}{"kind":"check_unavailable"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var result UpdateStatusResult
			if err := json.Unmarshal([]byte(raw), &result); err == nil {
				t.Fatalf("Unmarshal(%s) unexpectedly succeeded: %+v", raw, result)
			}
		})
	}
}

func TestUpdateStatusRequestAndResponseAreStrictValidatedContracts(t *testing.T) {
	request := UpdateStatusRequest{}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate request: %v", err)
	}
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	if string(encodedRequest) != `{}` {
		t.Fatalf("request JSON = %s, want {}", encodedRequest)
	}
	for _, raw := range []string{`null`, `{"unknown":true}`, `{} {}`} {
		var decoded UpdateStatusRequest
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Fatalf("request accepted invalid JSON %s", raw)
		}
	}

	want := UpdateStatusResponse{Result: AvailableUpdateStatusResult("1.2.3", "1.3.0")}
	if err := want.Validate(); err != nil {
		t.Fatalf("Validate response: %v", err)
	}
	encodedResponse, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}
	if string(encodedResponse) != `{"result":{"kind":"available","current_version":"1.2.3","latest_version":"1.3.0"}}` {
		t.Fatalf("response JSON = %s", encodedResponse)
	}
	var decodedResponse UpdateStatusResponse
	if err := json.Unmarshal(encodedResponse, &decodedResponse); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if err := decodedResponse.Validate(); err != nil {
		t.Fatalf("Validate decoded response: %v", err)
	}
	for _, raw := range []string{
		`{}`,
		`null`,
		`{"result":null}`,
		`{"result":{"kind":"unknown"}}`,
		`{"result":{"kind":"check_unavailable"},"unknown":true}`,
		`{"result":{"kind":"check_unavailable"}} {}`,
	} {
		var response UpdateStatusResponse
		if err := json.Unmarshal([]byte(raw), &response); err == nil {
			t.Fatalf("response accepted invalid JSON %s", raw)
		}
	}
}

func assertUpdateStatusResultAccessors(t *testing.T, result UpdateStatusResult) {
	t.Helper()
	switch result.Kind() {
	case UpdateStatusCurrent, UpdateStatusAvailable:
		current, latest, ok := result.Versions()
		if !ok || current == "" || latest == "" {
			t.Fatal("version result did not expose structurally present versions")
		}
		if _, ok := result.FailureCause(); ok {
			t.Fatal("version result exposed a failure cause")
		}
	case UpdateStatusCheckUnavailable:
		if _, _, ok := result.Versions(); ok {
			t.Fatal("check-unavailable result exposed versions")
		}
		if _, ok := result.FailureCause(); ok {
			t.Fatal("check-unavailable result exposed a failure cause")
		}
	case UpdateStatusCheckFailed:
		if _, _, ok := result.Versions(); ok {
			t.Fatal("check-failed result exposed versions")
		}
		cause, ok := result.FailureCause()
		if !ok || cause == "" {
			t.Fatal("check-failed result did not expose its structurally present cause")
		}
	default:
		t.Fatalf("unexpected kind %q", result.Kind())
	}
}
