package application

import "testing"

func TestRewritePath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "rewrite kapi root", input: "/kapi", want: "/api"},
		{name: "rewrite kapi child", input: "/kapi/v1/pods", want: "/api/v1/pods"},
		{name: "rewrite kapis root", input: "/kapis", want: "/apis"},
		{name: "rewrite kapis child", input: "/kapis/apps/v1/deployments", want: "/apis/apps/v1/deployments"},
		{name: "unsupported prefix", input: "/api/v1/user", wantErr: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := RewritePath(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.input)
				}
				return
			}

			if err != nil {
				t.Fatalf("RewritePath(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("RewritePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
