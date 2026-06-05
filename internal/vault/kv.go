package vault

import (
	"context"
	"encoding/hex"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"
)

type apiKV struct{ api *vaultapi.Client }

func (a *apiKV) Get(ctx context.Context, path string) ([]byte, int, error) {
	s, err := a.api.KVv2(mountPath).Get(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	return decodeKey(s)
}

func (a *apiKV) GetVersion(ctx context.Context, path string, version int) ([]byte, error) {
	s, err := a.api.KVv2(mountPath).GetVersion(ctx, path, version)
	if err != nil {
		return nil, err
	}
	b, _, err := decodeKey(s)
	return b, err
}

func (a *apiKV) Put(ctx context.Context, path string, key []byte) (int, error) {
	s, err := a.api.KVv2(mountPath).Put(ctx, path, map[string]any{
		"key": hex.EncodeToString(key),
	})
	if err != nil {
		return 0, err
	}
	if s == nil || s.VersionMetadata == nil {
		return 0, fmt.Errorf("vault: put returned no version metadata")
	}
	return s.VersionMetadata.Version, nil
}

func (a *apiKV) DeleteAll(ctx context.Context, path string) error {
	return a.api.KVv2(mountPath).DeleteMetadata(ctx, path)
}

func decodeKey(s *vaultapi.KVSecret) ([]byte, int, error) {
	if s == nil || s.Data == nil {
		return nil, 0, errNotFound
	}
	enc, ok := s.Data["key"].(string)
	if !ok {
		return nil, 0, fmt.Errorf("vault: key field missing or wrong type")
	}
	raw, err := hex.DecodeString(enc)
	if err != nil {
		return nil, 0, fmt.Errorf("vault: decode key: %w", err)
	}
	v := 0
	if s.VersionMetadata != nil {
		v = s.VersionMetadata.Version
	}
	return raw, v, nil
}
