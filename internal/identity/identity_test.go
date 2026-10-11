package identity

import (
	"context"
	"github.com/golang-jwt/jwt/v5"
	"strings"
	"testing"
	"time"
)

func TestRolesAndJWTRejectBeforeDatabase(t *testing.T) {
	a, err := New(nil, []byte(strings.Repeat("k", 32)), strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(context.Background(), "Bearer "+strings.Repeat("b", 32))
	if err != nil || p.Role != "admin" {
		t.Fatal(p, err)
	}
	if a.Allowed(Principal{Role: "reader"}, "approval.decide") || a.Allowed(Principal{Role: "writer"}, "token.issue") || !a.Allowed(Principal{Role: "reviewer"}, "approval.decide") {
		t.Fatal("policy escalation")
	}
	for _, method := range []jwt.SigningMethod{jwt.SigningMethodNone, jwt.SigningMethodHS384, jwt.SigningMethodHS256} {
		c := claims{Principal: Principal{Subject: "u", Tenant: "t", Project: "p", Role: "reader"}, RegisteredClaims: jwt.RegisteredClaims{Issuer: "other", Audience: jwt.ClaimStrings{"other"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}}
		key := any(a.Key)
		if method == jwt.SigningMethodNone {
			key = jwt.UnsafeAllowNoneSignatureType
		}
		token, _ := jwt.NewWithClaims(method, c).SignedString(key)
		if _, err = a.Authenticate(context.Background(), "Bearer "+token); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}
