package kvfs_test

import (
	"testing"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/stretchr/testify/assert"
)

func TestShardedKey(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		hash   string
		want   string
	}{
		{
			name:   "cas_prefix",
			prefix: kvfs.CasPrefix,
			hash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			want:   "cas/e3/b0/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:   "manifest_prefix",
			prefix: kvfs.ManifestPrefix,
			hash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			want:   "manifests/e3/b0/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, kvfs.ShardedKey(tt.prefix, tt.hash))
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
			err := kvfs.ValidateHash(tt.hash)
			if tt.wantErr {
				assert.ErrorIs(t, err, kvfs.ErrInvalidHash)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
