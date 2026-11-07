package healer

import "testing"

func TestBuildStepLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"#14 [certify-next builder 4/7] RUN npm ci", "certify-next builder 4/7 · RUN npm ci"},
		{"#14 3.21 [certify-next builder 4/7] RUN npm ci", "certify-next builder 4/7 · RUN npm ci"},
		{"#5 [1/4] FROM node:22-alpine", "1/4 · FROM node:22-alpine"},
		{"#14 CACHED", ""},
		{"#12 2.345 npm WARN deprecated something", ""},
		{"Step 4/12 : RUN go build ./...", "step 4/12 · RUN go build ./..."},
		{"", ""},
		{"writing image sha256:abc123", ""},
		{"DONE 2.5s", ""},
	}
	for _, c := range cases {
		if got := buildStepLine(c.in); got != c.want {
			t.Errorf("buildStepLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildStepLineTruncates(t *testing.T) {
	line := buildStepLine("#14 [stage 1/2] RUN " + string(make([]byte, 200)))
	if len(line) > 91 {
		t.Fatalf("line too long: %d", len(line))
	}
}
