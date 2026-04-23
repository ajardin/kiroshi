package gh

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_AuthenticatedUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantLogin  string
		wantErr    error
		wantErrSub string
	}{
		{
			name: "happy path",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user" {
					t.Errorf("path = %q, want /user", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("authorization header = %q", got)
				}
				fmt.Fprint(w, `{"login": "octocat"}`)
			},
			wantLogin: "octocat",
		},
		{
			name: "unauthorized maps to ErrInvalidToken",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message": "Bad credentials"}`)
			},
			wantErr: ErrInvalidToken,
		},
		{
			name: "server error is wrapped",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"message": "boom"}`)
			},
			wantErrSub: "fetch authenticated user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			c := newClient("test-token", srv.URL+"/")
			user, err := c.AuthenticatedUser(t.Context())

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want errors.Is(%v)", err, tt.wantErr)
				}
			case tt.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErrSub)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err = %v", err)
				}
				if user.Login != tt.wantLogin {
					t.Errorf("login = %q, want %q", user.Login, tt.wantLogin)
				}
			}
		})
	}
}
