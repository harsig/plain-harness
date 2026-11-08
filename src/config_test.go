package main

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestExpandEnv(t *testing.T) {
	t.Setenv("KEY", "secret")
	tests := []struct {
		name    string
		yaml    string
		want    string
		wantErr string
	}{
		{name: "value", yaml: "a: x-${KEY}\n", want: "a: x-secret\n"},
		{name: "escaped", yaml: "a: $${KEY}\n", want: "a: ${KEY}\n"},
		{name: "map key untouched", yaml: "${KEY}: v\n", want: "${KEY}: v\n"},
		{name: "nested list", yaml: "a: [\"${KEY}\"]\n", want: "a: [\"secret\"]\n"},
		{name: "unset", yaml: "a: ${PLAIN_HARNESS_TEST_UNSET}\n", wantErr: "PLAIN_HARNESS_TEST_UNSET is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tt.yaml), &doc); err != nil {
				t.Fatal(err)
			}
			err := expandEnv(&doc, false)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := yaml.Marshal(&doc)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
