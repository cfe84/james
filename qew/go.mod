module james/qew

go 1.25.6

require (
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/go-webauthn/webauthn v0.17.4
	github.com/google/uuid v1.6.0
	github.com/yuin/goldmark v1.7.8
	golang.org/x/crypto v0.52.0
	golang.org/x/net v0.55.0
	james/moneypenny v0.0.0
)

replace james/moneypenny => ../moneypenny

require (
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.2.6 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/sys v0.45.0 // indirect
)
