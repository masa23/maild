package main

import (
	"testing"
)

func TestDecodeSubject(t *testing.T) {
	tests := []struct {
		input         string
		expected      string
		expectedError bool
	}{
		{
			input:         "Hello ! konichiwa",
			expected:      "Hello ! konichiwa",
			expectedError: false,
		},
		{
			input:         "=?iso-2022-jp?b?GyRCJUYlOSVIJWEhPCVrGyhC?=",
			expected:      "テストメール",
			expectedError: false,
		},
		{
			input:         "=?UTF-8?B?44OG44K544OI44Gn44GZ44CC?=",
			expected:      "テストです。",
			expectedError: false,
		},
		{
			input:         "=?UTF-8?Q?=E3=81=93=E3=82=8C=E3=81=AF=E3=83=86=E3=82=B9=E3=83=88?=",
			expected:      "これはテスト",
			expectedError: false,
		},
	}

	for _, tt := range tests {
		got, err := decodeHeader(tt.input)
		if (err != nil) != tt.expectedError {
			t.Errorf("decodeHeader(%q) error = %v; want error? %v", tt.input, err, tt.expectedError)
			continue
		}
		if got != tt.expected {
			t.Errorf("decodeHeader(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}
