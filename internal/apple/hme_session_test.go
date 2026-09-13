package apple

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestListAliasesMissingServiceKeepsAuthenticationDistinct(t *testing.T) {
	client, err := NewClient(Config{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("missing service must be detected before a network request")
		return nil, errors.New("unexpected request")
	})})
	if err != nil {
		t.Fatal(err)
	}
	session := Session{Region: RegionGlobal, AppleID: "fixture@icloud.com", DSID: "42", SessionToken: "fixture-token"}
	_, returned, err := client.ListAliases(context.Background(), session)
	if !errors.Is(err, ErrHMEUnavailable) || errors.Is(err, ErrInvalidSession) || returned.SessionToken != session.SessionToken {
		t.Fatalf("missing HME service error = %v", err)
	}
}
