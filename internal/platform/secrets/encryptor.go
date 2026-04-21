package secrets

type Encryptor interface {
	Encrypt(value string) (string, error)
	Decrypt(value string) (string, error)
}

type NoopEncryptor struct{}

func (NoopEncryptor) Encrypt(value string) (string, error) {
	return value, nil
}

func (NoopEncryptor) Decrypt(value string) (string, error) {
	return value, nil
}
