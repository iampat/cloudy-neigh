package kvfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCASKey(t *testing.T) {
	tests := []struct {
		name string
		hash string
		want string
	}{
		{
			"valid_sha256",
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"cas/e3/b0/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, casKey(tt.hash))
		})
	}
}

func TestValidateHash(t *testing.T) {
	tests := []struct {
		name    string
		hash    string
		wantErr bool
	}{
		{"empty", "", true},
		{"short", "abcdef", true},
		{"uppercase", "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855", true},
		{"non_hex", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85g", true},
		{"valid", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHash(tt.hash)
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrInvalidHash)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
