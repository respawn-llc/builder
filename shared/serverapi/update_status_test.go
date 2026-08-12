package serverapi_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"core/shared/jsoncontract"
	. "core/shared/serverapi"
	"core/shared/serverjsoncontract"
)

func TestUpdateStatusResponseContractRejectsNullResult(t *testing.T) {
	contract, err := serverjsoncontract.PrepareUpdateStatusResponse(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare Update Status response contract: %v", err)
	}
	if _, err := contract.Decode([]byte(`{"result":null}`)); err == nil {
		t.Fatal("Update Status response contract accepted null result")
	}
}

func TestUpdateStatusResultJSONRoundTrip(t *testing.T) {
	contract, err := serverjsoncontract.PrepareUpdateStatusResponse(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare Update Status response contract: %v", err)
	}
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

			decoded, err := contract.Decode([]byte(`{"result":` + string(encoded) + `}`))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			roundTrip, err := json.Marshal(decoded.Result)
			if err != nil {
				t.Fatalf("Marshal decoded: %v", err)
			}
			if !bytes.Equal(roundTrip, encoded) {
				t.Fatalf("decoded JSON = %s, want %s", roundTrip, encoded)
			}
			assertUpdateStatusResultAccessors(t, decoded.Result)
		})
	}
}

func TestUpdateStatusResultRejectsInvalidJSON(t *testing.T) {
	contract, err := serverjsoncontract.PrepareUpdateStatusResponse(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare Update Status response contract: %v", err)
	}
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
			responseRaw := []byte(`{"result":` + raw + `}`)
			if raw == `{"kind":"check_unavailable"}{"kind":"check_unavailable"}` {
				responseRaw = []byte(`{"result":{"kind":"check_unavailable"}} {}`)
			}
			if _, err := contract.Decode(responseRaw); err == nil {
				t.Fatalf("Decode(%s) unexpectedly succeeded", raw)
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
	contract, err := serverjsoncontract.PrepareUpdateStatusResponse(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare Update Status response contract: %v", err)
	}
	decodedResponse, err := contract.Decode(encodedResponse)
	if err != nil {
		t.Fatalf("Decode response: %v", err)
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
		if _, err := contract.Decode([]byte(raw)); err == nil {
			t.Fatalf("response accepted invalid JSON %s", raw)
		}
	}
}

func assertUpdateStatusResultAccessors(t *testing.T, result UpdateStatusResult) {
	t.Helper()
	switch result.Kind() {
	case UpdateStatusCurrent, UpdateStatusAvailable:
		versions := result.Versions()
		if versions == nil || versions.Current == "" || versions.Latest == "" {
			t.Fatal("version result did not expose structurally present versions")
		}
		if result.Failure() != nil {
			t.Fatal("version result exposed a failure cause")
		}
	case UpdateStatusCheckUnavailable:
		if result.Versions() != nil {
			t.Fatal("check-unavailable result exposed versions")
		}
		if result.Failure() != nil {
			t.Fatal("check-unavailable result exposed a failure cause")
		}
	case UpdateStatusCheckFailed:
		if result.Versions() != nil {
			t.Fatal("check-failed result exposed versions")
		}
		failure := result.Failure()
		if failure == nil || failure.Cause == "" {
			t.Fatal("check-failed result did not expose its structurally present cause")
		}
	default:
		t.Fatalf("unexpected kind %q", result.Kind())
	}
}
